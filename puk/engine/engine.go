package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/welkin-moon/sing-box/puk/model"
)

const (
	maxProbeConcurrency = 4
	maxProbeBodyBytes   = int64(8 << 20)
	maxControllerBytes  = int64(1 << 20)
	controllerTimeout   = 3 * time.Second
	defaultTimeout      = 10 * time.Second
	maxTimeout          = 30 * time.Second
)

// Engine serializes probes and explicit selections so that controller state,
// hysteresis counters, and rollback decisions cannot race one another.
type Engine struct {
	controller       *url.URL
	controllerSecret string
	interval         time.Duration
	policyOrder      []*policyRuntime
	policies         map[string]*policyRuntime
	controllerHTTP   *http.Client

	operationMu sync.Mutex
	stateMu     sync.RWMutex
	started     bool
	dialer      func(context.Context, string, string) (net.Conn, error)

	// tlsConfig is nil in normal operation, which makes net/http use the system
	// roots and verify certificates. It is kept private to permit package tests
	// to trust a local test CA without weakening production verification.
	tlsConfig *tls.Config
}

type policyRuntime struct {
	config        model.Policy
	candidates    []*candidateRuntime
	byTag         map[string]*candidateRuntime
	current       string
	knownNow      bool
	holdDownTag   string
	holdDownUntil time.Time
}

type candidateRuntime struct {
	config      model.Candidate
	state       model.State
	latencyMS   float64
	latencySeen bool
}

type candidateRef struct {
	policy    *policyRuntime
	candidate *candidateRuntime
}

type probeJob struct {
	ref   candidateRef
	index int
}

type probeCompletion struct {
	job    probeJob
	result model.Result
}

type candidateRound struct {
	results   []model.Result
	completed int
	latencyMS int64
}

// Status is a credential-free snapshot of engine state.
type Status struct {
	Policies []PolicyStatus `json:"policies"`
}

type PolicyStatus struct {
	Selector   string            `json:"selector"`
	Current    string            `json:"current,omitempty"`
	Candidates []CandidateStatus `json:"candidates"`
}

type CandidateStatus struct {
	Tag       string         `json:"tag"`
	Healthy   bool           `json:"healthy"`
	Failures  int            `json:"failures"`
	Successes int            `json:"successes"`
	CheckedAt time.Time      `json:"checked_at,omitempty"`
	Results   []model.Result `json:"results,omitempty"`
}

// New validates and copies cfg. Candidate proxy URLs are required and can only
// use HTTP or HTTPS proxies; an absent proxy can therefore never become a
// direct connection.
func New(cfg model.Config) (*Engine, error) {
	controller, err := parseController(cfg.Controller)
	if err != nil {
		return nil, err
	}
	if len(cfg.Policies) == 0 {
		return nil, errors.New("at least one policy is required")
	}
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}

	e := &Engine{
		controller:       controller,
		controllerSecret: cfg.ControllerSecret,
		interval:         interval,
		policies:         make(map[string]*policyRuntime, len(cfg.Policies)),
	}
	e.controllerHTTP = newControllerClient(nil)
	for _, configured := range cfg.Policies {
		p, err := makePolicy(configured)
		if err != nil {
			return nil, err
		}
		if _, exists := e.policies[p.config.Selector]; exists {
			return nil, fmt.Errorf("duplicate selector %q", p.config.Selector)
		}
		e.policies[p.config.Selector] = p
		e.policyOrder = append(e.policyOrder, p)
	}
	return e, nil
}

func parseController(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("controller must be an HTTP(S) URL without user info, query, or fragment")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("cleartext controller must use a loopback address; remote controllers require HTTPS")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if i := strings.LastIndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func makePolicy(config model.Policy) (*policyRuntime, error) {
	config.Candidates = append([]model.Candidate(nil), config.Candidates...)
	config.Probes = append([]model.Probe(nil), config.Probes...)
	config.Selector = strings.TrimSpace(config.Selector)
	if config.Selector == "" || strings.ContainsAny(config.Selector, "\r\n") {
		return nil, errors.New("policy selector is required")
	}
	if len(config.Candidates) == 0 {
		return nil, fmt.Errorf("policy %q requires at least one candidate", config.Selector)
	}
	if len(config.Probes) == 0 {
		return nil, fmt.Errorf("policy %q requires at least one probe", config.Selector)
	}
	if config.Failures <= 0 {
		config.Failures = 1
	}
	if config.Recoveries <= 0 {
		config.Recoveries = 1
	}
	if config.HoldSeconds < 0 {
		return nil, fmt.Errorf("policy %q hold_seconds cannot be negative", config.Selector)
	}

	p := &policyRuntime{config: config, byTag: make(map[string]*candidateRuntime, len(config.Candidates))}
	for _, candidate := range config.Candidates {
		candidate.Tag = strings.TrimSpace(candidate.Tag)
		if candidate.Tag == "" || strings.ContainsAny(candidate.Tag, "\r\n") {
			return nil, fmt.Errorf("policy %q has an invalid candidate tag", config.Selector)
		}
		proxyURL, err := url.Parse(candidate.ProxyURL)
		if err != nil || proxyURL == nil || proxyURL.Host == "" || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") || proxyURL.Fragment != "" {
			return nil, fmt.Errorf("policy %q candidate %q requires a valid HTTP(S) proxy URL", config.Selector, candidate.Tag)
		}
		if _, exists := p.byTag[candidate.Tag]; exists {
			return nil, fmt.Errorf("policy %q has duplicate candidate tag %q", config.Selector, candidate.Tag)
		}
		candidate.ProxyURL = proxyURL.String()
		c := &candidateRuntime{config: candidate}
		p.candidates = append(p.candidates, c)
		p.byTag[candidate.Tag] = c
	}
	for i := range config.Probes {
		probe := &config.Probes[i]
		probeURL, err := url.Parse(probe.URL)
		if err != nil || probeURL == nil || probeURL.Scheme != "https" || probeURL.Host == "" || probeURL.User != nil || probeURL.Fragment != "" {
			return nil, fmt.Errorf("policy %q probe %d must use an HTTPS URL without user info or fragment", config.Selector, i)
		}
		if probe.MinBytes < 0 {
			return nil, fmt.Errorf("policy %q probe %d min_bytes cannot be negative", config.Selector, i)
		}
		if len(probe.Statuses) == 0 {
			return nil, fmt.Errorf("policy %q probe %d requires an explicit status allowlist", config.Selector, i)
		}
		seen := make(map[int]struct{}, len(probe.Statuses))
		for _, status := range probe.Statuses {
			if status < 100 || status > 599 {
				return nil, fmt.Errorf("policy %q probe %d has an invalid allowed status", config.Selector, i)
			}
			seen[status] = struct{}{}
		}
		probe.Statuses = append([]int(nil), probe.Statuses...)
		probe.TimeoutSeconds = int(normalizedTimeout(probe.TimeoutSeconds) / time.Second)
	}
	p.config = config
	return p, nil
}

func normalizedTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultTimeout
	}
	d := time.Duration(seconds) * time.Second
	if d > maxTimeout {
		d = maxTimeout
	}
	return d
}

// SetDialer installs the host's socket dialer, for example one that protects
// Android VpnService sockets. It must be called before the first Probe,
// Select, or Run call. The same dialer is used for Clash and proxy sockets.
func (e *Engine) SetDialer(dial func(context.Context, string, string) (net.Conn, error)) error {
	e.operationMu.Lock()
	defer e.operationMu.Unlock()
	if e.started {
		return errors.New("dialer must be set before the engine starts")
	}
	if e.controllerHTTP != nil {
		e.controllerHTTP.CloseIdleConnections()
	}
	e.dialer = dial
	e.controllerHTTP = newControllerClient(dial)
	return nil
}

// Status returns an independent snapshot and never includes candidate proxy
// URLs, controller credentials, or probe URL query strings.
func (e *Engine) Status() any {
	e.stateMu.RLock()
	defer e.stateMu.RUnlock()

	status := Status{Policies: make([]PolicyStatus, 0, len(e.policyOrder))}
	for _, p := range e.policyOrder {
		ps := PolicyStatus{Selector: p.config.Selector, Current: p.current, Candidates: make([]CandidateStatus, 0, len(p.candidates))}
		for _, candidate := range p.candidates {
			cs := CandidateStatus{
				Tag:       candidate.config.Tag,
				Healthy:   candidate.state.Healthy,
				Failures:  candidate.state.Failures,
				Successes: candidate.state.Successes,
				CheckedAt: candidate.state.CheckedAt,
				Results:   append([]model.Result(nil), candidate.state.Results...),
			}
			for i := range cs.Results {
				cs.Results[i].URL = safeProbeOrigin(cs.Results[i].URL)
			}
			ps.Candidates = append(ps.Candidates, cs)
		}
		status.Policies = append(status.Policies, ps)
	}
	return status
}

func safeProbeOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Probe checks each configured probe for every candidate and applies the
// configured failure/recovery streaks before considering a controller change.
func (e *Engine) Probe(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	e.operationMu.Lock()
	defer e.operationMu.Unlock()
	e.started = true

	// Refresh every selector before probe work begins. This both observes
	// external Clash changes and gives the scheduler a current route to retain.
	controllerErrors := make(map[*policyRuntime]error)
	for _, p := range e.policyOrder {
		if err := e.ensureCurrent(ctx, p); err != nil {
			controllerErrors[p] = err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var currentCandidates []candidateRef
	var remainingCandidates []candidateRef
	currentSeen := make(map[*candidateRuntime]bool)
	for _, p := range e.policyOrder {
		if current := p.byTag[p.current]; current != nil {
			currentCandidates = append(currentCandidates, candidateRef{policy: p, candidate: current})
			currentSeen[current] = true
		}
		for _, candidate := range p.candidates {
			if !currentSeen[candidate] {
				remainingCandidates = append(remainingCandidates, candidateRef{policy: p, candidate: candidate})
			}
		}
	}
	sort.SliceStable(remainingCandidates, func(i, j int) bool {
		a, b := remainingCandidates[i].candidate, remainingCandidates[j].candidate
		if a.config.Priority != b.config.Priority {
			return a.config.Priority > b.config.Priority
		}
		if a.config.Cost != b.config.Cost {
			return a.config.Cost < b.config.Cost
		}
		if a.latencySeen && b.latencySeen && a.latencyMS != b.latencyMS {
			return a.latencyMS < b.latencyMS
		}
		if a.config.Tag != b.config.Tag {
			return a.config.Tag < b.config.Tag
		}
		return remainingCandidates[i].policy.config.Selector < remainingCandidates[j].policy.config.Selector
	})
	candidates := append(currentCandidates, remainingCandidates...)
	totalJobs := 0
	for _, ref := range candidates {
		totalJobs += len(ref.policy.config.Probes)
	}
	if totalJobs == 0 {
		return firstPolicyError(e.policyOrder, controllerErrors)
	}

	jobs := make(chan probeJob, totalJobs)
	for _, ref := range candidates {
		for probeIndex := range ref.policy.config.Probes {
			jobs <- probeJob{ref: ref, index: probeIndex}
		}
	}
	close(jobs)

	transports := make(map[*candidateRuntime]*http.Transport, len(candidates))
	rounds := make(map[*candidateRuntime]*candidateRound, len(candidates))
	for _, ref := range candidates {
		proxyURL, _ := url.Parse(ref.candidate.config.ProxyURL) // validated by New
		transports[ref.candidate] = e.newTransport(proxyURL)
		rounds[ref.candidate] = &candidateRound{results: make([]model.Result, len(ref.policy.config.Probes))}
	}

	workerCount := totalJobs
	if workerCount > maxProbeConcurrency {
		workerCount = maxProbeConcurrency
	}
	completions := make(chan probeCompletion, totalJobs)
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		for _, transport := range transports {
			transport.CloseIdleConnections()
		}
	}()
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					probe := job.ref.policy.config.Probes[job.index]
					result := e.runProbe(ctx, transports[job.ref.candidate], probe)
					select {
					case completions <- probeCompletion{job: job, result: result}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	for finished := 0; finished < totalJobs; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case completion := <-completions:
			finished++
			candidate := completion.job.ref.candidate
			round := rounds[candidate]
			round.results[completion.job.index] = completion.result
			round.completed++
			round.latencyMS += completion.result.DurationMS
			if round.completed != len(round.results) {
				continue
			}
			allOK := true
			for _, result := range round.results {
				if result.Error != "" {
					allOK = false
					break
				}
			}
			e.recordCandidateRound(completion.job.ref.policy, candidate, round, allOK)
			p := completion.job.ref.policy
			preferred := preferredCandidate(p)
			if preferred != nil && preferred.config.Tag != p.current {
				if err := e.selectLocked(ctx, p, preferred.config.Tag); err != nil {
					controllerErrors[p] = err
				} else {
					delete(controllerErrors, p)
				}
			}
		}
	}
	return firstPolicyError(e.policyOrder, controllerErrors)
}

func (e *Engine) recordCandidateRound(p *policyRuntime, candidate *candidateRuntime, round *candidateRound, allOK bool) {
	latency := float64(round.latencyMS) / float64(len(round.results))
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	candidate.state.Results = round.results
	candidate.state.CheckedAt = time.Now().UTC()
	if allOK {
		candidate.state.Failures = 0
		candidate.state.Successes++
		if candidate.state.Successes >= p.config.Recoveries {
			candidate.state.Healthy = true
		}
		candidate.latencyMS = latency
		candidate.latencySeen = true
	} else {
		candidate.state.Successes = 0
		candidate.state.Failures++
		if candidate.state.Failures >= p.config.Failures {
			candidate.state.Healthy = false
		}
	}
}

func firstPolicyError(order []*policyRuntime, errs map[*policyRuntime]error) error {
	for _, p := range order {
		if err := errs[p]; err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) newTransport(proxyURL *url.URL) *http.Transport {
	transport := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DialContext:       e.dialer,
		ForceAttemptHTTP2: true,
	}
	if e.tlsConfig != nil {
		transport.TLSClientConfig = e.tlsConfig.Clone()
	}
	return transport
}

func (e *Engine) runProbe(parent context.Context, transport *http.Transport, probe model.Probe) model.Result {
	started := time.Now()
	result := model.Result{URL: probe.URL}
	timeout := normalizedTimeout(probe.TimeoutSeconds)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, nil)
	if err != nil {
		result.Error = "invalid probe request"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		result.Error = classifyRequestError(err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	result.Status = response.StatusCode
	count, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxProbeBodyBytes+1))
	closeErr := response.Body.Close()
	result.Bytes = count
	result.DurationMS = time.Since(started).Milliseconds()
	if readErr != nil || closeErr != nil {
		result.Error = "body_read_failed"
		return result
	}
	if count > maxProbeBodyBytes {
		result.Bytes = maxProbeBodyBytes
		result.Error = "body_exceeds_limit"
		return result
	}
	if !statusAllowed(response.StatusCode, probe.Statuses) {
		result.Error = "http_status"
		return result
	}
	if count < probe.MinBytes {
		result.Error = "body_below_minimum"
	}
	return result
}

func classifyRequestError(err error) string {
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return "timeout"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "tls:") || strings.Contains(message, "x509:") {
		return "tls"
	}
	if strings.Contains(message, "proxyconnect") || strings.Contains(message, "proxy error") {
		return "proxy_connect"
	}
	return "request_failed"
}

func statusAllowed(status int, allow []int) bool {
	for _, permitted := range allow {
		if status == permitted {
			return true
		}
	}
	return false
}

func preferredCandidate(p *policyRuntime) *candidateRuntime {
	current := p.byTag[p.current]
	if current != nil && (current.state.Healthy || current.state.Failures < p.config.Failures) {
		return current
	}
	var healthy []*candidateRuntime
	for _, candidate := range p.candidates {
		if candidate.state.Healthy {
			healthy = append(healthy, candidate)
		}
	}
	if len(healthy) == 0 {
		return nil
	}
	if time.Now().Before(p.holdDownUntil) && p.holdDownTag != "" {
		eligible := healthy[:0]
		for _, candidate := range healthy {
			if candidate.config.Tag != p.holdDownTag {
				eligible = append(eligible, candidate)
			}
		}
		if len(eligible) > 0 {
			healthy = eligible
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		a, b := healthy[i], healthy[j]
		if a.config.Priority != b.config.Priority {
			return a.config.Priority > b.config.Priority
		}
		if a.config.Cost != b.config.Cost {
			return a.config.Cost < b.config.Cost
		}
		if a.latencySeen && b.latencySeen && a.latencyMS != b.latencyMS {
			return a.latencyMS < b.latencyMS
		}
		if a.latencySeen != b.latencySeen {
			return a.latencySeen
		}
		return a.config.Tag < b.config.Tag
	})
	return healthy[0]
}

// Select explicitly changes a known selector to a known candidate tag.
func (e *Engine) Select(ctx context.Context, selector, tag string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	e.operationMu.Lock()
	defer e.operationMu.Unlock()
	p, ok := e.policies[selector]
	if !ok {
		return fmt.Errorf("unknown selector %q", selector)
	}
	if _, ok := p.byTag[tag]; !ok {
		return fmt.Errorf("unknown candidate tag %q for selector %q", tag, selector)
	}
	e.started = true
	return e.selectLocked(ctx, p, tag)
}

func (e *Engine) selectLocked(ctx context.Context, p *policyRuntime, tag string) error {
	if err := e.ensureCurrent(ctx, p); err != nil {
		return err
	}
	if p.current == tag {
		return nil
	}
	previous := p.current
	if err := e.putSelection(ctx, p.config.Selector, tag); err != nil {
		if rollbackErr := e.rollbackSelection(ctx, p, previous); rollbackErr != nil {
			return errors.New("controller PUT failed and rollback could not be verified")
		}
		return err
	}
	now, err := e.getCurrent(ctx, p.config.Selector)
	if err != nil || now != tag {
		rollbackErr := e.rollbackSelection(ctx, p, previous)
		if err != nil {
			if rollbackErr != nil {
				return errors.New("selection verification failed and rollback could not be verified")
			}
			return errors.New("selection verification failed; rollback verified")
		}
		if rollbackErr != nil {
			return errors.New("selection verification disagreed and rollback could not be verified")
		}
		return errors.New("selection verification disagreed; rollback verified")
	}
	e.stateMu.Lock()
	p.current = tag
	p.knownNow = true
	if p.config.HoldSeconds > 0 {
		p.holdDownTag = previous
		p.holdDownUntil = time.Now().Add(time.Duration(p.config.HoldSeconds) * time.Second)
	} else {
		p.holdDownTag = ""
		p.holdDownUntil = time.Time{}
	}
	e.stateMu.Unlock()
	return nil
}

func (e *Engine) ensureCurrent(ctx context.Context, p *policyRuntime) error {
	now, err := e.getCurrent(ctx, p.config.Selector)
	if err != nil {
		e.stateMu.Lock()
		p.knownNow = false
		e.stateMu.Unlock()
		return err
	}
	e.stateMu.Lock()
	p.current = now
	p.knownNow = true
	e.stateMu.Unlock()
	return nil
}

func (e *Engine) rollbackSelection(ctx context.Context, p *policyRuntime, previous string) error {
	putErr := e.putSelection(ctx, p.config.Selector, previous)
	now, getErr := e.getCurrent(ctx, p.config.Selector)
	if getErr == nil && now == previous {
		e.stateMu.Lock()
		p.current = previous
		p.knownNow = true
		e.stateMu.Unlock()
		return nil
	}
	e.stateMu.Lock()
	p.knownNow = false
	e.stateMu.Unlock()
	if getErr != nil {
		return errors.New("rollback readback failed")
	}
	if putErr != nil {
		return errors.New("rollback PUT failed and readback disagreed")
	}
	return errors.New("rollback readback disagreed")
}

func (e *Engine) controllerEndpoint(selector string) *url.URL {
	u := *e.controller
	basePath := strings.TrimRight(u.EscapedPath(), "/")
	escaped := basePath + "/proxies/" + url.PathEscape(selector)
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		decoded = path.Join(u.Path, "proxies", selector)
		escaped = ""
	}
	u.Path = decoded
	u.RawPath = escaped
	return &u
}

func (e *Engine) getCurrent(ctx context.Context, selector string) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, controllerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, e.controllerEndpoint(selector).String(), nil)
	if err != nil {
		return "", errors.New("could not construct controller request")
	}
	e.authorize(req)
	response, err := e.controllerHTTP.Do(req)
	if err != nil {
		return "", errors.New("controller GET failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("controller GET returned HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxControllerBytes+1))
	if err != nil || int64(len(contents)) > maxControllerBytes {
		return "", errors.New("controller GET returned an invalid response")
	}
	var payload struct {
		Now string `json:"now"`
	}
	if err := json.Unmarshal(contents, &payload); err != nil || strings.TrimSpace(payload.Now) == "" {
		return "", errors.New("controller GET returned an invalid response")
	}
	return payload.Now, nil
}

func (e *Engine) putSelection(ctx context.Context, selector, tag string) error {
	requestCtx, cancel := context.WithTimeout(ctx, controllerTimeout)
	defer cancel()
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: tag})
	if err != nil {
		return errors.New("could not encode controller selection")
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPut, e.controllerEndpoint(selector).String(), strings.NewReader(string(body)))
	if err != nil {
		return errors.New("could not construct controller request")
	}
	req.Header.Set("Content-Type", "application/json")
	e.authorize(req)
	response, err := e.controllerHTTP.Do(req)
	if err != nil {
		return errors.New("controller PUT failed")
	}
	defer response.Body.Close()
	read, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxControllerBytes+1))
	if readErr != nil || read > maxControllerBytes {
		return errors.New("controller PUT returned an invalid response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("controller PUT returned HTTP %d", response.StatusCode)
	}
	return nil
}

func newControllerClient(dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{DialContext: dial},
		Timeout:   controllerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (e *Engine) authorize(req *http.Request) {
	if e.controllerSecret != "" {
		req.Header.Set("Authorization", "Bearer "+e.controllerSecret)
	}
}

// Run probes immediately and then at the configured interval. Transient
// controller and network errors are reflected by candidate state and retried
// on the next interval; cancellation ends the loop.
func (e *Engine) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	e.operationMu.Lock()
	e.started = true
	e.operationMu.Unlock()
	for {
		_ = e.Probe(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

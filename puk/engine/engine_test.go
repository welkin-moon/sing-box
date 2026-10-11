package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/welkin-moon/sing-box/puk/model"
)

type testController struct {
	server       *httptest.Server
	mu           sync.Mutex
	now          string
	puts         []string
	putNotify    chan string
	verify       string
	failRollback bool
}

func newTestController(t *testing.T, initial string) *testController {
	t.Helper()
	c := &testController{now: initial, putNotify: make(chan string, 16)}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer controller-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/proxies/main" {
			http.NotFound(w, r)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			current := c.now
			if c.verify != "" && len(c.puts) == 1 {
				current = c.verify
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "main", "now": current})
		case http.MethodPut:
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			c.puts = append(c.puts, body.Name)
			select {
			case c.putNotify <- body.Name:
			default:
			}
			if c.failRollback && len(c.puts) == 2 {
				http.Error(w, "rollback refused", http.StatusInternalServerError)
				return
			}
			c.now = body.Name
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *testController) currentAndPuts() (string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now, append([]string(nil), c.puts...)
}

func (c *testController) setVerification(now string) {
	c.mu.Lock()
	c.verify = now
	c.mu.Unlock()
}

func (c *testController) setCurrent(now string) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func (c *testController) rejectRollback() {
	c.mu.Lock()
	c.failRollback = true
	c.mu.Unlock()
}

func newTestTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return server, roots
}

func newConnectProxy(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var allow atomic.Bool
	allow.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		if !allow.Load() {
			http.Error(w, "proxy unavailable", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		client, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, 2*time.Second)
		if err != nil {
			_ = client.Close()
			return
		}
		if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		if err = rw.Flush(); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		go func() {
			_, _ = io.Copy(upstream, rw)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		_ = upstream.Close()
	}))
	t.Cleanup(server.Close)
	return server, &allow
}

func newOneShotBlockingProxy(t *testing.T, started, canceled chan struct{}) *httptest.Server {
	t.Helper()
	var block atomic.Bool
	block.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		if block.CompareAndSwap(true, false) {
			started <- struct{}{}
			<-r.Context().Done()
			close(canceled)
			return
		}
		serveTunnel(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func serveTunnel(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	upstream, err := net.DialTimeout("tcp", r.Host, 2*time.Second)
	if err != nil {
		_ = client.Close()
		return
	}
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if err = rw.Flush(); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	go func() {
		_, _ = io.Copy(upstream, rw)
		_ = upstream.Close()
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	_ = upstream.Close()
}

func makeEngine(t *testing.T, controllerURL string, candidates []model.Candidate, probes []model.Probe, failures, recoveries, hold int) *Engine {
	t.Helper()
	cfg := model.Config{
		Controller:       controllerURL,
		ControllerSecret: "controller-secret",
		Policies: []model.Policy{{
			Selector:    "main",
			Candidates:  candidates,
			Probes:      probes,
			Failures:    failures,
			Recoveries:  recoveries,
			HoldSeconds: hold,
		}},
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func probeFor(rawURL string) model.Probe {
	return model.Probe{URL: rawURL, Statuses: []int{http.StatusOK}, MinBytes: 4, TimeoutSeconds: 2}
}

func trustTestServer(t *testing.T, e *Engine, roots *x509.CertPool) {
	t.Helper()
	e.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

func TestProbeUsesExplicitProxyForVerifiedTLSAndStatusIsSafe(t *testing.T) {
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "healthy")
	}))
	proxy, _ := newConnectProxy(t)
	controller := newTestController(t, "route")
	e := makeEngine(t, controller.server.URL, []model.Candidate{{Tag: "route", ProxyURL: proxy.URL + "/" + "", Priority: 1}}, []model.Probe{probeFor(target.URL + "/health?api_key=probe-secret")}, 1, 1, 0)
	trustTestServer(t, e, roots)

	if err := e.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	status := e.Status().(Status)
	if len(status.Policies) != 1 || !status.Policies[0].Candidates[0].Healthy {
		t.Fatalf("expected healthy TLS probe through CONNECT proxy, got %#v", status)
	}
	if status.Policies[0].Candidates[0].Results[0].Bytes != int64(len("healthy")) {
		t.Fatalf("expected full body read, got %#v", status.Policies[0].Candidates[0].Results[0])
	}
	wrapped, err := json.Marshal(map[string]any{"routing": e.Status()})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"controller-secret", proxy.URL, "probe-secret"} {
		if strings.Contains(string(wrapped), secret) {
			t.Fatalf("status leaked %q: %s", secret, wrapped)
		}
	}
}

func TestNoDirectFallbackWhenProxyUnavailable(t *testing.T) {
	var directHits atomic.Int32
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		_, _ = io.WriteString(w, "healthy")
	}))
	deadProxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyURL := "http://" + deadProxy.Addr().String()
	_ = deadProxy.Close()
	controller := newTestController(t, "route")
	e := makeEngine(t, controller.server.URL, []model.Candidate{{Tag: "route", ProxyURL: proxyURL}}, []model.Probe{probeFor(target.URL)}, 1, 1, 0)
	trustTestServer(t, e, roots)

	if err := e.Probe(context.Background()); err != nil {
		t.Fatalf("Probe should record candidate failure without returning an engine error: %v", err)
	}
	if directHits.Load() != 0 {
		t.Fatalf("target was reached directly %d times", directHits.Load())
	}
	status := e.Status().(Status)
	if status.Policies[0].Candidates[0].Healthy {
		t.Fatalf("unreachable explicit proxy was marked healthy: %#v", status)
	}
}

func TestIncrementalSelectionCompletesFastCandidateWhileOneRequestBlocks(t *testing.T) {
	started := make(chan struct{}, 1)
	blockedCanceled := make(chan struct{})
	blockingProxy := newOneShotBlockingProxy(t, started, blockedCanceled)
	fastProxy, _ := newConnectProxy(t)
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "healthy")
	}))
	controller := newTestController(t, "slow")
	probes := []model.Probe{probeFor(target.URL), probeFor(target.URL), probeFor(target.URL)}
	e := makeEngine(t, controller.server.URL, []model.Candidate{
		{Tag: "slow", ProxyURL: blockingProxy.URL, Priority: 20},
		{Tag: "fast", ProxyURL: fastProxy.URL, Priority: 10},
	}, probes, 1, 1, 0)
	trustTestServer(t, e, roots)
	e.policies["main"].byTag["slow"].state.Failures = 1

	ctx, cancel := context.WithCancel(context.Background())
	probeDone := make(chan error, 1)
	go func() { probeDone <- e.Probe(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("the unrelated request did not block")
	}
	select {
	case selected := <-controller.putNotify:
		if selected != "fast" {
			cancel()
			t.Fatalf("expected the fast candidate, selected %q", selected)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("engine waited for the blocked request instead of selecting after the fast candidate completed")
	}
	cancel()
	if err := <-probeDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation to stop remaining probe work, got %v", err)
	}
	select {
	case <-blockedCanceled:
	case <-time.After(time.Second):
		t.Fatal("blocked proxy request was not canceled")
	}
	if current, _ := controller.currentAndPuts(); current != "fast" {
		t.Fatalf("incremental selection was not committed, current=%q", current)
	}
}

func TestFailureRecoveryHoldDownAndPriorityRanking(t *testing.T) {
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "healthy")
	}))
	proxyA, allowA := newConnectProxy(t)
	proxyB, _ := newConnectProxy(t)
	proxyC, allowC := newConnectProxy(t)
	controller := newTestController(t, "A")
	e := makeEngine(t, controller.server.URL, []model.Candidate{
		{Tag: "A", ProxyURL: proxyA.URL, Priority: 30},
		{Tag: "B", ProxyURL: proxyB.URL, Priority: 10},
		{Tag: "C", ProxyURL: proxyC.URL, Priority: 20},
	}, []model.Probe{probeFor(target.URL)}, 2, 2, 30)
	trustTestServer(t, e, roots)

	// Two consecutive healthy rounds are required before any candidate becomes
	// eligible, matching the configured recovery threshold.
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, puts := controller.currentAndPuts(); current != "A" || len(puts) != 0 {
		t.Fatalf("healthy current route should be retained, current=%q puts=%v", current, puts)
	}

	allowA.Store(false)
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := e.Status().(Status)
	if !status.Policies[0].Candidates[0].Healthy || status.Policies[0].Candidates[0].Failures != 1 {
		t.Fatalf("candidate should require two failed rounds: %#v", status.Policies[0].Candidates[0])
	}
	if current, _ := controller.currentAndPuts(); current != "A" {
		t.Fatalf("selected too early after one failure: %q", current)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, puts := controller.currentAndPuts(); current != "C" || len(puts) != 1 || puts[0] != "C" {
		t.Fatalf("expected ranked failover to C, current=%q puts=%v", current, puts)
	}

	allowA.Store(true)
	allowC.Store(false)
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, _ := controller.currentAndPuts(); current != "C" {
		t.Fatalf("one C failure should be held by the failure threshold, current=%q", current)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, puts := controller.currentAndPuts(); current != "B" || len(puts) != 2 || puts[1] != "B" {
		t.Fatalf("hold-down should avoid immediately returning to recovered A, current=%q puts=%v", current, puts)
	}
}

func TestProbeAndManualSelectRefreshExternalControllerState(t *testing.T) {
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "healthy")
	}))
	proxy, _ := newConnectProxy(t)
	controller := newTestController(t, "A")
	e := makeEngine(t, controller.server.URL, []model.Candidate{
		{Tag: "A", ProxyURL: proxy.URL, Priority: 2},
		{Tag: "B", ProxyURL: proxy.URL, Priority: 1},
	}, []model.Probe{probeFor(target.URL)}, 1, 1, 0)
	trustTestServer(t, e, roots)

	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	controller.setCurrent("B")
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := e.Status().(Status)
	if status.Policies[0].Current != "B" {
		t.Fatalf("Probe did not refresh controller state, current=%q", status.Policies[0].Current)
	}
	if current, puts := controller.currentAndPuts(); current != "B" || len(puts) != 0 {
		t.Fatalf("external change to a healthy candidate should be retained, current=%q puts=%v", current, puts)
	}

	controller.setCurrent("A")
	if err := e.Select(context.Background(), "main", "A"); err != nil {
		t.Fatal(err)
	}
	status = e.Status().(Status)
	if status.Policies[0].Current != "A" {
		t.Fatalf("manual Select did not refresh controller state, current=%q", status.Policies[0].Current)
	}
	if current, puts := controller.currentAndPuts(); current != "A" || len(puts) != 0 {
		t.Fatalf("manual selection matching external state should be a no-op, current=%q puts=%v", current, puts)
	}
}

func TestSelectionVerificationMismatchRollsBack(t *testing.T) {
	controller := newTestController(t, "A")
	controller.setVerification("A")
	e := makeEngine(t, controller.server.URL, []model.Candidate{
		{Tag: "A", ProxyURL: "http://127.0.0.1:1"},
		{Tag: "B", ProxyURL: "http://127.0.0.1:2"},
	}, []model.Probe{probeFor("https://example.test/health")}, 1, 1, 0)

	err := e.Select(context.Background(), "main", "B")
	if err == nil || !strings.Contains(err.Error(), "verification disagreed") {
		t.Fatalf("expected verification mismatch, got %v", err)
	}
	current, puts := controller.currentAndPuts()
	if current != "A" || len(puts) != 2 || puts[0] != "B" || puts[1] != "A" {
		t.Fatalf("expected PUT B followed by rollback PUT A, current=%q puts=%v", current, puts)
	}
	statusJSON, marshalErr := json.Marshal(e.Status())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(statusJSON), "127.0.0.1:1") || strings.Contains(string(statusJSON), "127.0.0.1:2") {
		t.Fatalf("status exposed proxy URLs: %s", statusJSON)
	}
}

func TestUnverifiedRollbackMarksControllerStateUnknown(t *testing.T) {
	controller := newTestController(t, "A")
	controller.setVerification("unexpected")
	controller.rejectRollback()
	e := makeEngine(t, controller.server.URL, []model.Candidate{
		{Tag: "A", ProxyURL: "http://127.0.0.1:1"},
		{Tag: "B", ProxyURL: "http://127.0.0.1:2"},
	}, []model.Probe{probeFor("https://example.test/health")}, 1, 1, 0)

	err := e.Select(context.Background(), "main", "B")
	if err == nil || !strings.Contains(err.Error(), "rollback could not be verified") {
		t.Fatalf("expected unverified rollback error, got %v", err)
	}
	if e.policies["main"].knownNow {
		t.Fatal("controller state must be marked unknown when rollback readback disagrees")
	}
	if err := e.Select(context.Background(), "main", "A"); err != nil {
		t.Fatalf("subsequent selection should refresh and recover from unknown state: %v", err)
	}
	current, _ := controller.currentAndPuts()
	if current != "A" {
		t.Fatalf("expected explicit selection to recover controller state, current=%q", current)
	}
}

func TestSetDialerCoversControllerAndProxyAndLocksAfterStart(t *testing.T) {
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "healthy")
	}))
	proxy, _ := newConnectProxy(t)
	controller := newTestController(t, "route")
	e := makeEngine(t, controller.server.URL, []model.Candidate{{Tag: "route", ProxyURL: proxy.URL}}, []model.Probe{probeFor(target.URL)}, 1, 1, 0)
	trustTestServer(t, e, roots)
	var mu sync.Mutex
	var dialed []string
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	if err := e.SetDialer(dial); err != nil {
		t.Fatalf("SetDialer: %v", err)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.SetDialer(dial); err == nil {
		t.Fatal("SetDialer after Probe should fail")
	}
	mu.Lock()
	seenController, seenProxy := false, false
	for _, address := range dialed {
		seenController = seenController || strings.HasSuffix(address, strings.TrimPrefix(controller.server.URL, "http://"))
		seenProxy = seenProxy || strings.HasSuffix(address, strings.TrimPrefix(proxy.URL, "http://"))
	}
	mu.Unlock()
	if !seenController || !seenProxy {
		t.Fatalf("dialer did not see both controller and proxy sockets: %v", dialed)
	}
}

func TestProbeConcurrencyIsGloballyBoundedAnd403NeedsAllowlist(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	target, roots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, "healthy")
		active.Add(-1)
	}))
	proxy, _ := newConnectProxy(t)
	controller := newTestController(t, "route")
	probes := make([]model.Probe, 9)
	for i := range probes {
		probes[i] = probeFor(fmt.Sprintf("%s/health/%d", target.URL, i))
	}
	e := makeEngine(t, controller.server.URL, []model.Candidate{{Tag: "route", ProxyURL: proxy.URL}}, probes, 1, 1, 0)
	trustTestServer(t, e, roots)
	if err := e.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got > maxProbeConcurrency || got < 2 {
		t.Fatalf("expected concurrent but globally bounded probes, maximum=%d", got)
	}

	for _, probe := range probes {
		probe.Statuses = []int{http.StatusForbidden}
		probe.MinBytes = 0
		break
	}
	// A separate 403-only endpoint confirms that 403 succeeds only when it is
	// explicitly included in the probe's status allowlist.
	forbiddenTarget, forbiddenRoots := newTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "denied")
	}))
	_ = forbiddenRoots
	deniedProbe := probeFor(forbiddenTarget.URL)
	deniedProbe.Statuses = []int{http.StatusOK}
	deniedProbe.MinBytes = 0
	deniedEngine := makeEngine(t, controller.server.URL, []model.Candidate{{Tag: "route", ProxyURL: proxy.URL}}, []model.Probe{deniedProbe}, 1, 1, 0)
	trustTestServer(t, deniedEngine, forbiddenRoots)
	if err := deniedEngine.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if deniedEngine.Status().(Status).Policies[0].Candidates[0].Healthy {
		t.Fatal("HTTP 403 was accepted without appearing in the status allowlist")
	}
	result := deniedEngine.Status().(Status).Policies[0].Candidates[0].Results[0]
	if result.Error != "http_status" || result.Status != http.StatusForbidden {
		t.Fatalf("HTTP status failure should have a stable error code, got %#v", result)
	}
}

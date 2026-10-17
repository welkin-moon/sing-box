// Package tunnel supervises cloudflared Access TCP processes.
//
// Listener readiness only confirms that a local TCP connection can be opened.
// It does not verify that cloudflared has authenticated or that the remote
// Access application is reachable. Callers should use their normal HTTPS
// candidate probes for end-to-end health.
package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/welkin-moon/sing-box/puk/core"
	"github.com/welkin-moon/sing-box/puk/model"
)

const (
	stateStopped   = "stopped"
	stateStarting  = "starting"
	stateListening = "local_listener_ready"
	stateBackoff   = "backoff"

	errorStartFailed  = "process_start_failed"
	errorExitedEarly  = "process_exited_before_listener"
	errorExited       = "process_exited"
	errorReadyTimeout = "local_listener_timeout"
	errorListenInUse  = "listen_endpoint_in_use"

	statusWarning = "Authentication is unverified; cloudflared may require interactive Access login."
)

const (
	defaultReadinessTimeout = 10 * time.Second
	defaultReadinessPoll    = 100 * time.Millisecond
	defaultDialTimeout      = 200 * time.Millisecond
	defaultRestartBase      = 500 * time.Millisecond
	defaultRestartMax       = 30 * time.Second
	defaultStopGrace        = 2 * time.Second
)

// TunnelStatus contains only fixed, non-sensitive state. It deliberately omits
// the configured hostname, listen address, binary path, and process output.
type TunnelStatus struct {
	Index                  int    `json:"index"`
	State                  string `json:"state"`
	Attempts               uint64 `json:"attempts"`
	LocalListenerReady     bool   `json:"local_listener_ready"`
	AuthenticationVerified bool   `json:"authentication_verified"`
	Warning                string `json:"warning"`
	LastError              string `json:"last_error,omitempty"`
}

// Manager owns only the cloudflared processes it starts. It never searches for
// or signals other cloudflared processes on the host.
type Manager struct {
	mu       sync.Mutex
	configs  []model.Tunnel
	statuses []TunnelStatus
	started  bool
	cancel   context.CancelFunc
	done     chan struct{}

	newProcess func(binary string, args ...string) childProcess

	readinessTimeout time.Duration
	readinessPoll    time.Duration
	dialTimeout      time.Duration
	restartBase      time.Duration
	restartMax       time.Duration
	stopGrace        time.Duration
}

type childProcess interface {
	Start() error
	Wait() error
	Interrupt() error
	Kill() error
}

type commandProcess struct {
	cmd *exec.Cmd
}

func (p *commandProcess) Start() error { return p.cmd.Start() }
func (p *commandProcess) Wait() error  { return p.cmd.Wait() }
func (p *commandProcess) Interrupt() error {
	return p.cmd.Process.Signal(os.Interrupt)
}
func (p *commandProcess) Kill() error { return p.cmd.Process.Kill() }

// New validates all tunnel definitions without starting any processes.
func New(configs []model.Tunnel) (*Manager, error) {
	copyOfConfigs := append([]model.Tunnel(nil), configs...)
	seenListen := make(map[string]struct{}, len(copyOfConfigs))
	for i, config := range copyOfConfigs {
		if err := validateHostname(config.Hostname); err != nil {
			return nil, tunnelConfigError(i, "invalid hostname")
		}
		listenKey, err := validateListen(config.Listen)
		if err != nil {
			return nil, tunnelConfigError(i, "listen address must use a loopback IP or localhost and a fixed TCP port")
		}
		if _, exists := seenListen[listenKey]; exists {
			return nil, tunnelConfigError(i, "duplicate listen address")
		}
		seenListen[listenKey] = struct{}{}
		if config.Binary == "" || !filepath.IsAbs(config.Binary) || strings.IndexByte(config.Binary, 0) >= 0 {
			return nil, tunnelConfigError(i, "binary must be an absolute path")
		}
	}

	m := &Manager{
		configs:          copyOfConfigs,
		statuses:         make([]TunnelStatus, len(copyOfConfigs)),
		readinessTimeout: defaultReadinessTimeout,
		readinessPoll:    defaultReadinessPoll,
		dialTimeout:      defaultDialTimeout,
		restartBase:      defaultRestartBase,
		restartMax:       defaultRestartMax,
		stopGrace:        defaultStopGrace,
	}
	m.newProcess = func(binary string, args ...string) childProcess {
		cmd := exec.Command(binary, args...)
		core.HideProcessWindow(cmd)
		// cloudflared output may contain credentials or login URLs. Keep it out
		// of the parent process and out of all status/error surfaces.
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return &commandProcess{cmd: cmd}
	}
	for i := range m.statuses {
		m.statuses[i] = TunnelStatus{
			Index:                  i,
			State:                  stateStopped,
			AuthenticationVerified: false,
			Warning:                statusWarning,
		}
	}
	return m, nil
}

func tunnelConfigError(index int, reason string) error {
	return errors.New("tunnel configuration at index " + strconv.Itoa(index) + ": " + reason)
}

func validateHostname(host string) error {
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return errors.New("invalid DNS hostname")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return errors.New("hostname must be a fully qualified DNS name")
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid DNS label")
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return errors.New("invalid DNS hostname character")
			}
		}
	}
	return nil
}

func validateListen(address string) (string, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.TrimSpace(host) != host {
		return "", errors.New("invalid TCP endpoint")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid TCP port")
	}
	canonicalHost := strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return "", errors.New("listen IP is not loopback")
		}
		canonicalHost = ip.String()
	} else if canonicalHost != "localhost" {
		return "", errors.New("listen host is not loopback")
	}
	return net.JoinHostPort(canonicalHost, strconv.Itoa(port)), nil
}

// Start starts all supervisors and waits for each one to complete its first
// bounded local-listener attempt. A startup error is sanitized; supervision
// continues with capped backoff so a transient failure can recover.
func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("tunnel manager already started")
	}
	m.started = true
	workerCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	initial := make(chan bool, len(m.configs))
	var workers sync.WaitGroup
	workers.Add(len(m.configs))
	for i := range m.configs {
		go m.supervise(workerCtx, i, initial, &workers)
	}
	go func() {
		workers.Wait()
		close(m.done)
	}()
	m.mu.Unlock()

	allReady := true
	for range m.configs {
		select {
		case ready := <-initial:
			allReady = allReady && ready
		case <-workerCtx.Done():
			return workerCtx.Err()
		}
	}
	if !allReady {
		return errors.New("one or more tunnel processes did not reach a local TCP listener; supervision continues")
	}
	return nil
}

// Stop cancels the supervisors, gives owned child processes a short chance to
// exit on interrupt, then kills and waits for any process that remains.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

// Status returns an ordered snapshot. It contains no configured endpoint,
// binary path, raw process error, stderr, or authentication material.
func (m *Manager) Status() any {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]TunnelStatus, len(m.statuses))
	copy(result, m.statuses)
	return result
}

func (m *Manager) supervise(ctx context.Context, index int, initial chan<- bool, workers *sync.WaitGroup) {
	defer workers.Done()
	initialReported := false
	reportInitial := func(ready bool) {
		if !initialReported {
			initial <- ready
			initialReported = true
		}
	}
	var attempts uint64
	config := m.configs[index]

	for {
		if ctx.Err() != nil {
			m.setStopped(index)
			reportInitial(false)
			return
		}
		attempts++
		m.updateStatus(index, func(status *TunnelStatus) {
			status.State = stateStarting
			status.Attempts = attempts
			status.LocalListenerReady = false
			status.LastError = ""
		})
		if !listenAddressAvailable(config.Listen) {
			m.setBackoff(index, errorListenInUse)
			reportInitial(false)
			if !waitContext(ctx, m.backoff(attempts)) {
				m.setStopped(index)
				return
			}
			continue
		}

		process := m.newProcess(config.Binary, "access", "tcp", "--hostname", config.Hostname, "--url", config.Listen)
		if process == nil || process.Start() != nil {
			m.setBackoff(index, errorStartFailed)
			reportInitial(false)
			if !waitContext(ctx, m.backoff(attempts)) {
				m.setStopped(index)
				return
			}
			continue
		}

		waitResult := make(chan error, 1)
		go func() { waitResult <- process.Wait() }()
		outcome := m.awaitListener(ctx, config.Listen, waitResult)
		if outcome == readinessCanceled {
			m.stopProcess(process, waitResult)
			m.setStopped(index)
			reportInitial(false)
			return
		}
		if outcome != readinessSucceeded {
			if outcome == readinessTimedOut {
				m.stopProcess(process, waitResult)
				m.setBackoff(index, errorReadyTimeout)
			} else {
				m.setBackoff(index, errorExitedEarly)
			}
			reportInitial(false)
			if !waitContext(ctx, m.backoff(attempts)) {
				m.setStopped(index)
				return
			}
			continue
		}

		m.updateStatus(index, func(status *TunnelStatus) {
			status.State = stateListening
			status.LocalListenerReady = true
			status.LastError = ""
		})
		reportInitial(true)
		select {
		case <-ctx.Done():
			m.stopProcess(process, waitResult)
			m.setStopped(index)
			return
		case <-waitResult:
			m.setBackoff(index, errorExited)
		}
		if ctx.Err() != nil {
			m.setStopped(index)
			return
		}
		if !waitContext(ctx, m.backoff(attempts)) {
			m.setStopped(index)
			return
		}
	}
}

type readinessOutcome uint8

const (
	readinessSucceeded readinessOutcome = iota
	readinessExited
	readinessTimedOut
	readinessCanceled
)

func (m *Manager) awaitListener(ctx context.Context, address string, waitResult <-chan error) readinessOutcome {
	timer := time.NewTimer(m.readinessTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(m.readinessPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return readinessCanceled
		case <-waitResult:
			return readinessExited
		case <-timer.C:
			return readinessTimedOut
		case <-ticker.C:
			if !canConnect(address, m.dialTimeout) {
				continue
			}
			select {
			case <-waitResult:
				return readinessExited
			default:
				return readinessSucceeded
			}
		}
	}
}

func canConnect(address string, timeout time.Duration) bool {
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func listenAddressAvailable(address string) bool {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func (m *Manager) stopProcess(process childProcess, waitResult <-chan error) {
	select {
	case <-waitResult:
		return
	default:
	}
	_ = process.Interrupt()
	timer := time.NewTimer(m.stopGrace)
	select {
	case <-waitResult:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return
	case <-timer.C:
	}
	_ = process.Kill()
	<-waitResult
}

func (m *Manager) backoff(attempt uint64) time.Duration {
	delay := m.restartBase
	for i := uint64(1); i < attempt && delay < m.restartMax; i++ {
		if delay > m.restartMax/2 {
			return m.restartMax
		}
		delay *= 2
	}
	if delay > m.restartMax {
		return m.restartMax
	}
	return delay
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *Manager) updateStatus(index int, update func(*TunnelStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	update(&m.statuses[index])
}

func (m *Manager) setBackoff(index int, category string) {
	m.updateStatus(index, func(status *TunnelStatus) {
		status.State = stateBackoff
		status.LocalListenerReady = false
		status.LastError = category
	})
}

func (m *Manager) setStopped(index int) {
	m.updateStatus(index, func(status *TunnelStatus) {
		status.State = stateStopped
		status.LocalListenerReady = false
	})
}

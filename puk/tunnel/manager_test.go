package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/welkin-moon/sing-box/puk/model"
)

func validConfig(t *testing.T) model.Tunnel {
	t.Helper()
	binary, err := filepath.Abs("cloudflared.exe")
	if err != nil {
		t.Fatal(err)
	}
	return model.Tunnel{
		Hostname: "app.example.com",
		Listen:   "127.0.0.1:41000",
		Binary:   binary,
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.Tunnel)
	}{
		{name: "scheme", mutate: func(c *model.Tunnel) { c.Hostname = "https://app.example.com" }},
		{name: "path", mutate: func(c *model.Tunnel) { c.Hostname = "app.example.com/path" }},
		{name: "hostname option injection", mutate: func(c *model.Tunnel) { c.Hostname = "--help.example" }},
		{name: "wildcard listen", mutate: func(c *model.Tunnel) { c.Listen = "0.0.0.0:1234" }},
		{name: "non-loopback listen", mutate: func(c *model.Tunnel) { c.Listen = "192.0.2.10:1234" }},
		{name: "zero port", mutate: func(c *model.Tunnel) { c.Listen = "127.0.0.1:0" }},
		{name: "relative binary", mutate: func(c *model.Tunnel) { c.Binary = "cloudflared" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig(t)
			if test.name != "zero port" {
				config.Listen = "127.0.0.1:41001"
			}
			test.mutate(&config)
			if _, err := New([]model.Tunnel{config}); err == nil {
				t.Fatal("New accepted invalid configuration")
			}
		})
	}
}

func TestNewAcceptsLoopbackEndpointsAndRejectsDuplicate(t *testing.T) {
	for _, address := range []string{"127.0.0.1:41001", "[::1]:41002", "localhost:41003"} {
		config := validConfig(t)
		config.Listen = address
		if _, err := New([]model.Tunnel{config}); err != nil {
			t.Errorf("New(%q): %v", address, err)
		}
	}
	config := validConfig(t)
	config.Listen = "127.0.0.1:41004"
	if _, err := New([]model.Tunnel{config, config}); err == nil {
		t.Fatal("New accepted duplicate listen endpoints")
	}
}

func TestCommandUsesExactArgumentsAndDiscardsOutput(t *testing.T) {
	manager, err := New([]model.Tunnel{validConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	const binary = "/path with spaces/cloudflared"
	args := []string{"access", "tcp", "--hostname", "app.example.com", "--url", "127.0.0.1:41001"}
	process := manager.newProcess(binary, args...).(*commandProcess)
	if process.cmd.Path != binary {
		t.Fatalf("command path = %q, want %q", process.cmd.Path, binary)
	}
	if !reflect.DeepEqual(process.cmd.Args, append([]string{binary}, args...)) {
		t.Fatalf("command args = %#v", process.cmd.Args)
	}
	if process.cmd.Stdout != io.Discard || process.cmd.Stderr != io.Discard {
		t.Fatal("command output is not discarded")
	}
}

func TestSupervisorReadinessStatusAndGracefulStop(t *testing.T) {
	config := validConfig(t)
	config.Listen = freeLoopbackAddress(t)
	manager, err := New([]model.Tunnel{config})
	if err != nil {
		t.Fatal(err)
	}
	manager.readinessTimeout = time.Second
	manager.readinessPoll = 5 * time.Millisecond
	manager.restartBase = 5 * time.Millisecond
	manager.stopGrace = 100 * time.Millisecond

	var child *fakeChild
	var gotBinary string
	var gotArgs []string
	manager.newProcess = func(binary string, args ...string) childProcess {
		gotBinary = binary
		gotArgs = append([]string(nil), args...)
		child = newFakeChild(config.Listen, true)
		return child
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wantArgs := []string{"access", "tcp", "--hostname", config.Hostname, "--url", config.Listen}
	if gotBinary != config.Binary || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("process invocation = %q %#v", gotBinary, gotArgs)
	}

	status := manager.Status().([]TunnelStatus)
	if len(status) != 1 || status[0].State != stateListening || !status[0].LocalListenerReady {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status[0].AuthenticationVerified || !strings.Contains(status[0].Warning, "interactive Access login") {
		t.Fatalf("status claims authentication is verified: %#v", status[0])
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{config.Hostname, config.Listen, config.Binary, "token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status leaked %q: %s", secret, encoded)
		}
	}

	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if child.interrupts != 1 || child.kills != 0 {
		t.Fatalf("shutdown did not use graceful interrupt: interrupts=%d kills=%d", child.interrupts, child.kills)
	}
	status = manager.Status().([]TunnelStatus)
	if status[0].State != stateStopped || status[0].LocalListenerReady {
		t.Fatalf("unexpected stopped status: %#v", status[0])
	}
}

func TestSupervisorDoesNotTouchAnExistingListener(t *testing.T) {
	address := freeLoopbackAddress(t)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	config := validConfig(t)
	config.Listen = address
	manager, err := New([]model.Tunnel{config})
	if err != nil {
		t.Fatal(err)
	}
	manager.restartBase = time.Second
	processCalls := 0
	manager.newProcess = func(string, ...string) childProcess {
		processCalls++
		return newFakeChild(address, true)
	}
	if err := manager.Start(context.Background()); err == nil {
		t.Fatal("Start reported an existing listener as managed readiness")
	}
	if processCalls != 0 {
		t.Fatalf("supervisor launched %d process(es) while endpoint was occupied", processCalls)
	}
	status := manager.Status().([]TunnelStatus)[0]
	if status.LastError != errorListenInUse {
		t.Fatalf("unexpected occupied endpoint status: %#v", status)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("existing listener was disturbed: %v", err)
	}
	_ = connection.Close()
}

func TestReadinessTimeoutIsBoundedAndReportedSanitized(t *testing.T) {
	config := validConfig(t)
	config.Listen = freeLoopbackAddress(t)
	manager, err := New([]model.Tunnel{config})
	if err != nil {
		t.Fatal(err)
	}
	manager.readinessTimeout = 30 * time.Millisecond
	manager.readinessPoll = 2 * time.Millisecond
	manager.restartBase = time.Second
	manager.stopGrace = 20 * time.Millisecond
	manager.newProcess = func(string, ...string) childProcess { return newFakeChild(config.Listen, false) }

	started := time.Now()
	if err := manager.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded without a local listener")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("readiness check was not bounded: %s", elapsed)
	}
	status := manager.Status().([]TunnelStatus)[0]
	if status.LastError != errorReadyTimeout || status.LocalListenerReady {
		t.Fatalf("unexpected timeout status: %#v", status)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	manager := &Manager{restartBase: 10 * time.Millisecond, restartMax: 25 * time.Millisecond}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond, 25 * time.Millisecond}
	for i, expected := range want {
		if actual := manager.backoff(uint64(i + 1)); actual != expected {
			t.Errorf("backoff(%d) = %s, want %s", i+1, actual, expected)
		}
	}
}

func TestStopKillsAndWaitsWhenInterruptIsIgnored(t *testing.T) {
	config := validConfig(t)
	config.Listen = freeLoopbackAddress(t)
	manager, err := New([]model.Tunnel{config})
	if err != nil {
		t.Fatal(err)
	}
	manager.readinessTimeout = time.Second
	manager.readinessPoll = 2 * time.Millisecond
	manager.stopGrace = 10 * time.Millisecond
	var child *fakeChild
	manager.newProcess = func(string, ...string) childProcess {
		child = newFakeChild(config.Listen, true)
		child.ignoreInterrupt = true
		return child
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := manager.Stop(); err != nil {
		t.Fatal(err)
	}
	child.mu.Lock()
	interrupts, kills := child.interrupts, child.kills
	child.mu.Unlock()
	if interrupts != 1 || kills != 1 {
		t.Fatalf("shutdown did not interrupt, kill, and wait: interrupts=%d kills=%d", interrupts, kills)
	}
}

func TestSupervisorRestartsAfterEarlyExit(t *testing.T) {
	config := validConfig(t)
	config.Listen = freeLoopbackAddress(t)
	manager, err := New([]model.Tunnel{config})
	if err != nil {
		t.Fatal(err)
	}
	manager.readinessTimeout = time.Second
	manager.readinessPoll = 2 * time.Millisecond
	manager.restartBase = 2 * time.Millisecond
	manager.restartMax = 10 * time.Millisecond
	var processCalls atomic.Uint32
	manager.newProcess = func(string, ...string) childProcess {
		if processCalls.Add(1) == 1 {
			child := newFakeChild(config.Listen, false)
			child.close()
			return child
		}
		return newFakeChild(config.Listen, true)
	}
	if err := manager.Start(context.Background()); err == nil {
		t.Fatal("Start did not report the first process exit")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := manager.Status().([]TunnelStatus)[0]
		if status.State == stateListening {
			if status.Attempts < 2 || processCalls.Load() < 2 {
				t.Fatalf("supervisor did not retry: status=%#v calls=%d", status, processCalls.Load())
			}
			if err := manager.Stop(); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("supervisor did not recover; latest status: %#v", manager.Status())
}

type fakeChild struct {
	address         string
	listener        net.Listener
	done            chan struct{}
	started         bool
	ignoreInterrupt bool
	interrupts      int
	kills           int
	mu              sync.Mutex
	once            sync.Once
}

func newFakeChild(address string, listen bool) *fakeChild {
	return &fakeChild{address: address, done: make(chan struct{}), started: listen}
}

func (p *fakeChild) Start() error {
	if !p.started {
		return nil
	}
	listener, err := net.Listen("tcp", p.address)
	if err != nil {
		return err
	}
	p.listener = listener
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return nil
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func (p *fakeChild) Wait() error {
	<-p.done
	return nil
}

func (p *fakeChild) Interrupt() error {
	p.mu.Lock()
	p.interrupts++
	ignoreInterrupt := p.ignoreInterrupt
	p.mu.Unlock()
	if !ignoreInterrupt {
		p.close()
	}
	return nil
}

func (p *fakeChild) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.close()
	return nil
}

func (p *fakeChild) close() {
	p.once.Do(func() {
		if p.listener != nil {
			_ = p.listener.Close()
		}
		close(p.done)
	})
}

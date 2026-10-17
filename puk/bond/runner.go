//go:build !js

package bond

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Runner controls only the core it starts; no process-name kill or global proxy changes.
type Runner struct {
	closed bool
	mu     sync.Mutex
	bin    string
	cmd    *exec.Cmd
	done   chan struct{}
}

func NewRunner(bin string) (*Runner, error) {
	a, e := filepath.Abs(bin)
	if e != nil {
		return nil, e
	}
	s, e := os.Stat(a)
	if e != nil || s.IsDir() {
		return nil, errors.New("core executable does not exist")
	}
	return &Runner{bin: a}, nil
}
func (r *Runner) Start(ctx context.Context, raw []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("editor is shutting down")
	}
	if r.cmd != nil {
		return errors.New("stop the current connection before applying another profile")
	}
	l, e := Load(raw)
	if e != nil {
		return e
	}
	c, e := l.NativeConfig()
	if e != nil {
		return e
	}
	// Fail before spawning if another app already owns the intended local port.
	probe, e := net.Listen("tcp4", "127.0.0.1:19080")
	if e != nil {
		return errors.New("local proxy port 19080 is in use")
	}
	probe.Close()
	dir, e := os.MkdirTemp("", "puk-bond-")
	if e != nil {
		return e
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(dir)
		}
	}()
	b, _ := json.Marshal(c)
	path := filepath.Join(dir, "config.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		return e
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	check := exec.CommandContext(checkCtx, r.bin, "check", "-c", path)
	check.Stderr = os.Stderr
	if e = check.Run(); e != nil {
		return errors.New("core rejected configuration; see the local startup window")
	}
	cmd := exec.CommandContext(ctx, r.bin, "run", "-c", path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		return errors.New("could not start core")
	}
	done := make(chan struct{})
	r.cmd = cmd
	r.done = done
	cleanup = false
	go func() {
		cmd.Wait()
		os.RemoveAll(dir)
		r.mu.Lock()
		if r.cmd == cmd {
			r.cmd = nil
		}
		r.mu.Unlock()
		close(done)
	}()
	return nil
}
func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd, done := r.cmd, r.done
	r.mu.Unlock()
	if cmd == nil {
		return nil
	}
	cmd.Process.Kill()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("core shutdown timed out")
	}
}
func (r *Runner) Running() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.cmd != nil }

func (r *Runner) Close() error { r.mu.Lock(); r.closed = true; r.mu.Unlock(); return r.Stop() }

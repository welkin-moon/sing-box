// Package mobile is a gomobile-compatible host boundary for SFA integration.
// The Android host must own VpnService and sing-box, and protect these sockets.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/welkin-moon/sing-box/puk/engine"
	"github.com/welkin-moon/sing-box/puk/model"
)

type SocketProtector interface{ Protect(fd int64) bool }
type Controller struct {
	mu     sync.Mutex
	engine *engine.Engine
	cancel context.CancelFunc
	done   chan struct{}
}

func NewController(configJSON string, protector SocketProtector) (*Controller, error) {
	if protector == nil {
		return nil, errors.New("Android VPN socket protector required")
	}
	var cfg model.Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, err
	}
	en, err := engine.New(cfg)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Control: func(network, address string, conn syscall.RawConn) error {
		protected := false
		if err := conn.Control(func(fd uintptr) { protected = protector.Protect(int64(fd)) }); err != nil {
			return err
		}
		if !protected {
			return errors.New("VPN socket protection failed")
		}
		return nil
	}}
	if err = en.SetDialer(dialer.DialContext); err != nil {
		return nil, err
	}
	return &Controller{engine: en}, nil
}
func (c *Controller) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return errors.New("already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() { defer close(c.done); c.engine.Run(ctx) }()
	return nil
}
func (c *Controller) Stop() {
	c.mu.Lock()
	if c.cancel == nil {
		c.mu.Unlock()
		return
	}
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	cancel()
	<-done
	c.mu.Lock()
	c.cancel = nil
	c.mu.Unlock()
}
func (c *Controller) StatusJSON() (string, error) {
	b, err := json.Marshal(c.engine.Status())
	return string(b), err
}
func (c *Controller) Probe() error { return c.engine.Probe(context.Background()) }
func (c *Controller) Select(selector, tag string) error {
	return c.engine.Select(context.Background(), selector, tag)
}

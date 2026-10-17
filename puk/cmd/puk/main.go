package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/welkin-moon/sing-box/puk/control"
	"github.com/welkin-moon/sing-box/puk/core"
	"github.com/welkin-moon/sing-box/puk/engine"
	"github.com/welkin-moon/sing-box/puk/model"
	"github.com/welkin-moon/sing-box/puk/tunnel"
)

const version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func load(path string) (model.Config, error) {
	var c model.Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func loopback(addr string) bool {
	h, _, e := net.SplitHostPort(addr)
	if e != nil {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: puk generate|run|status|probe|select|stop|tui|mcp|version")
	}
	if os.Args[1] == "version" {
		fmt.Println(version)
		return nil
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	config := f.String("config", "puk.json", "runtime configuration")
	input := f.String("input", "", "sing-box JSON subscription")
	output := f.String("out-dir", "runtime", "new output directory")
	base := f.Int("port", 19080, "main loopback mixed port; probe ports follow")
	binary := f.String("core", "", "absolute sing-box binary path (run only)")
	coreConfig := f.String("core-config", "", "sing-box configuration to start")
	selector := f.String("selector", "PUK-Auto", "selector")
	tag := f.String("tag", "", "candidate tag")
	attach := f.String("attach", "", "JSON tunnel attachments for generate")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if os.Args[1] == "generate" {
		if *input == "" {
			return errors.New("--input required")
		}
		raw, e := os.ReadFile(*input)
		if e != nil {
			return e
		}
		var tunnels []model.Tunnel
		if *attach != "" {
			b, e := os.ReadFile(*attach)
			if e != nil {
				return e
			}
			raw, tunnels, e = core.Attach(raw, b)
			if e != nil {
				return e
			}
		}
		sb, c, e := core.Generate(raw, *base)
		if e != nil {
			return e
		}
		c.Tunnels = tunnels
		// Each destination must be new, so updates cannot silently erase a working setup.
		if _, e = os.Stat(*output); e == nil {
			return errors.New("output directory exists; use a new version directory")
		}
		if !os.IsNotExist(e) {
			return e
		}
		if e = core.WriteNew(filepath.Join(*output, "sing-box.json"), sb); e != nil {
			return e
		}
		b, _ := json.MarshalIndent(c, "", "  ")
		if e = core.WriteNew(filepath.Join(*output, "puk.json"), b); e != nil {
			return e
		}
		fmt.Println("Generated owner-only configuration; original subscription untouched:", *output)
		return nil
	}
	c, e := load(*config)
	if e != nil {
		return e
	}
	if !loopback(c.Listen) || len(c.Token) < 32 {
		return errors.New("listen must be loopback and token at least 32 characters")
	}
	switch os.Args[1] {
	case "status", "probe", "select", "stop":
		method, path := http.MethodGet, "/api/status"
		var body any
		if os.Args[1] == "probe" {
			method, path = http.MethodPost, "/api/probe"
		}
		if os.Args[1] == "stop" {
			method, path = http.MethodPost, "/api/stop"
		}
		if os.Args[1] == "select" {
			if *tag == "" {
				return errors.New("--tag required")
			}
			method, path = http.MethodPost, "/api/select"
			body = map[string]string{"selector": *selector, "tag": *tag}
		}
		b, e := control.Request(ctx, "http://"+c.Listen, c.Token, method, path, body)
		if e != nil {
			return e
		}
		fmt.Println(string(b))
		return nil
	case "tui":
		return control.RunTUI(ctx, os.Stdin, os.Stdout, "http://"+c.Listen, c.Token)
	case "mcp":
		return control.ServeMCP(ctx, os.Stdin, os.Stdout, &remoteBackend{c: c})
	case "run":
	default:
		return errors.New("unknown command")
	}
	if (*binary == "") != (*coreConfig == "") {
		return errors.New("--core and --core-config must be provided together")
	}
	if *binary != "" {
		if !filepath.IsAbs(*binary) {
			return errors.New("--core must be absolute")
		}
		address := strings.TrimPrefix(c.Controller, "http://")
		if !loopback(address) {
			return errors.New("managed controller must use loopback HTTP")
		}
		preflight, err := net.Listen("tcp", address)
		if err != nil {
			return errors.New("managed controller port already in use")
		}
		preflight.Close()
		// Validation happens before starting either managed process.
		check := exec.CommandContext(ctx, *binary, "check", "-c", *coreConfig)
		core.HideProcessWindow(check)
		check.Stdout = os.Stderr
		check.Stderr = os.Stderr
		if e = check.Run(); e != nil {
			return fmt.Errorf("sing-box check failed: %w", e)
		}
	}
	listener, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return errors.New("dashboard port already in use")
	}
	defer listener.Close()
	manager, e := tunnel.New(c.Tunnels)
	if e != nil {
		return e
	}
	if e = manager.Start(ctx); e != nil {
		return e
	}
	defer manager.Stop()
	if *binary != "" {
		cmd := exec.CommandContext(ctx, *binary, "run", "-c", *coreConfig)
		core.HideProcessWindow(cmd)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			return e
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := cmd.Wait()
			if ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "managed core exited:", err)
				cancel()
			}
		}()
		defer wg.Wait()
		defer cancel()
		// Wait for the owned core before probing; startup races must not count
		// against otherwise healthy nodes.
		address := strings.TrimPrefix(c.Controller, "http://")
		if !loopback(address) {
			return errors.New("managed controller must use loopback HTTP")
		}
		readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
		defer readyCancel()
		if e = waitListener(readyCtx, address); e != nil {
			return e
		}
	}
	en, e := engine.New(c)
	if e != nil {
		return e
	}
	backend := &localBackend{en: en, tm: manager, cancel: cancel}
	server := &http.Server{Addr: c.Listen, Handler: control.New(backend, c.Token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		if err := en.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "routing engine:", err)
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		server.Shutdown(sctx)
	}()
	fmt.Fprintln(os.Stderr, "PUK dashboard: http://"+c.Listen+" (token in configuration file)")
	e = server.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	cancel()
	return e
}

type localBackend struct {
	en     *engine.Engine
	tm     *tunnel.Manager
	cancel context.CancelFunc
}

func (b *localBackend) Status() any {
	return map[string]any{"routing": b.en.Status(), "tunnels": b.tm.Status(), "version": version}
}
func (b *localBackend) Probe(ctx context.Context) error               { return b.en.Probe(ctx) }
func (b *localBackend) Select(ctx context.Context, s, t string) error { return b.en.Select(ctx, s, t) }
func (b *localBackend) Stop(ctx context.Context) error {
	time.AfterFunc(100*time.Millisecond, b.cancel)
	return nil
}

type remoteBackend struct{ c model.Config }

func (b *remoteBackend) Status() any {
	r, e := control.Request(context.Background(), "http://"+b.c.Listen, b.c.Token, "GET", "/api/status", nil)
	if e != nil {
		return map[string]string{"error": "daemon unavailable"}
	}
	var v any
	json.Unmarshal(r, &v)
	return v
}
func (b *remoteBackend) Probe(ctx context.Context) error {
	_, e := control.Request(ctx, "http://"+b.c.Listen, b.c.Token, "POST", "/api/probe", nil)
	return e
}
func (b *remoteBackend) Select(ctx context.Context, s, t string) error {
	_, e := control.Request(ctx, "http://"+b.c.Listen, b.c.Token, "POST", "/api/select", map[string]string{"selector": strings.TrimSpace(s), "tag": t})
	return e
}
func (b *remoteBackend) Stop(ctx context.Context) error {
	_, e := control.Request(ctx, "http://"+b.c.Listen, b.c.Token, "POST", "/api/stop", nil)
	return e
}
func waitListener(ctx context.Context, address string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		d := net.Dialer{Timeout: 100 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("managed core did not become ready")
		case <-ticker.C:
		}
	}
}

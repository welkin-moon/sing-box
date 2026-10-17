package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/welkin-moon/sing-box/puk/bond"
	"github.com/welkin-moon/sing-box/puk/core"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func write(dir, name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0600)
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: puk-bond init|ui|generate|run|bridge|server [--config FILE] [--out-dir NEW_DIR] [--core patched-sing-box]")
	}
	mode := os.Args[1]
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	file := f.String("config", "", "private library/server config")
	out := f.String("out-dir", "puk-bond-private", "new output directory")
	bin := f.String("core", "", "patched sing-box executable")
	listen := f.String("listen", "127.0.0.1:19080", "bridge SOCKS listener (loopback only)")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch mode {
	case "ui":
		return bond.ServeEditor(ctx, *bin)
	case "init":
		var l bond.Library
		if e := json.Unmarshal(bond.ExampleLibrary, &l); e != nil {
			return e
		}
		var e error
		l.Secret, e = bond.NewSecret()
		if e != nil {
			return e
		}
		if e = os.Mkdir(*out, 0700); e != nil {
			return e
		}
		if e = write(*out, "library.private.json", l); e != nil {
			return e
		}
		if e = write(*out, "server.private.json", l.ServerTemplate()); e != nil {
			return e
		}
		fmt.Println("Created local examples with a new private secret:", *out)
		return nil
	}
	raw, e := os.ReadFile(*file)
	if e != nil {
		return e
	}
	if mode == "server" {
		s, e := bond.LoadServer(raw)
		if e != nil {
			return e
		}
		return s.Run(ctx)
	}
	l, e := bond.Load(raw)
	if e != nil {
		return e
	}
	switch mode {
	case "generate":
		c, e := l.NativeConfig()
		if e != nil {
			return e
		}
		if e = os.Mkdir(*out, 0700); e != nil {
			return e
		}
		if e = write(*out, "sing-box.private.json", c); e != nil {
			return e
		}
		if e = write(*out, "server.private.json", l.ServerTemplate()); e != nil {
			return e
		}
		return write(*out, "library.private.json", l)
	case "run":
		if *bin == "" {
			return errors.New("--core must point to a sing-box build containing puk-bond")
		}
		c, e := l.NativeConfig()
		if e != nil {
			return e
		}
		dir, e := os.MkdirTemp("", "puk-bond-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(dir)
		if e = write(dir, "config.json", c); e != nil {
			return e
		}
		abs, e := filepath.Abs(*bin)
		if e != nil {
			return e
		}
		path := filepath.Join(dir, "config.json")
		check := exec.CommandContext(ctx, abs, "check", "-c", path)
		core.HideProcessWindow(check)
		if e = check.Run(); e != nil {
			return fmt.Errorf("core rejected config: %w", e)
		}
		cmd := exec.CommandContext(ctx, abs, "run", "-c", path)
		core.HideProcessWindow(cmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		e = cmd.Run()
		if ctx.Err() != nil {
			return nil
		}
		return e
	case "bridge":
		c, e := l.Client(nil)
		if e != nil {
			return e
		}
		ln, e := net.Listen("tcp", *listen)
		if e != nil {
			return e
		}
		defer ln.Close()
		return c.ServeSOCKS(ctx, ln)
	default:
		return errors.New("unknown command")
	}
}

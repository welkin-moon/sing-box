package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"github.com/welkin-moon/sing-box/puk/split"
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
		return errors.New("usage: puk-split generate|run --profile FILE [--out-dir NEW_DIR] [--xray FILE]")
	}
	mode := os.Args[1]
	if mode != "generate" && mode != "run" {
		return errors.New("unknown command")
	}
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	profile := f.String("profile", "", "private split profile JSON")
	out := f.String("out-dir", "split-runtime", "new output directory")
	xray := f.String("xray", "", "Xray executable, required for run")
	socks := f.Int("socks-port", 19080, "loopback SOCKS port")
	http := f.Int("http-port", 19081, "loopback HTTP proxy port")
	if e := f.Parse(os.Args[2:]); e != nil {
		return e
	}
	if *profile == "" {
		return errors.New("--profile required")
	}
	b, e := os.ReadFile(*profile)
	if e != nil {
		return e
	}
	p, e := split.Load(b)
	if e != nil {
		return e
	}
	c, e := split.Config(p, *socks, *http)
	if e != nil {
		return e
	}
	if mode == "generate" {
		if e = os.Mkdir(*out, 0700); e != nil {
			return fmt.Errorf("create new output directory: %w", e)
		}
		if e = write(*out, "xray.json", c); e != nil {
			return e
		}
		gui, e := split.Config(p, 0, 0)
		if e != nil {
			return e
		}
		if e = write(*out, "throne-configs.json", []any{gui}); e != nil {
			return e
		}
		link, e := split.ThroneLink(p)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(*out, "throne-import.txt"), []byte(link+"\n"), 0600); e != nil {
			return e
		}
		fmt.Println("Generated split runtime:", *out)
		return nil
	}
	if *xray == "" {
		return errors.New("--xray required; install a compatible Xray core explicitly")
	}
	bin, e := filepath.Abs(*xray)
	if e != nil {
		return e
	}
	if st, e := os.Stat(bin); e != nil || st.IsDir() {
		return errors.New("Xray executable does not exist")
	}
	dir, e := os.MkdirTemp("", "puk-split-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	if e = write(dir, "xray.json", c); e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	check := exec.CommandContext(ctx, bin, "run", "-test", "-config", filepath.Join(dir, "xray.json"))
	if b, e := check.CombinedOutput(); e != nil {
		return fmt.Errorf("Xray rejected config: %s: %w", b, e)
	}
	cmd := exec.CommandContext(ctx, bin, "run", "-config", filepath.Join(dir, "xray.json"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Printf("SOCKS %s / HTTP %s; Ctrl+C stops only this process.\n", split.LoopbackAddress(*socks), split.LoopbackAddress(*http))
	e = cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	return e
}

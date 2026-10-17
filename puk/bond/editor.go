package bond

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed editor.html
var EditorHTML string

//go:embed library.example.json
var ExampleLibrary []byte

func Compile(raw []byte) (map[string]any, error) {
	l, e := Load(raw)
	if e != nil {
		return nil, e
	}
	c, e := l.NativeConfig()
	if e != nil {
		return nil, e
	}
	return map[string]any{"library": l, "config": c, "server": l.ServerTemplate()}, nil
}
func editorHandler(token string) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.Replace(EditorHTML, "/*EXAMPLE*/null", strings.TrimSpace(string(ExampleLibrary)), 1))
	})
	m.HandleFunc("/api/compile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-PUK-Token")), []byte(token)) != 1 || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, `{"error":"forbidden"}`, 403)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":"JSON required"}`, 415)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if e != nil {
			http.Error(w, `{"error":"library too large"}`, 413)
			return
		}
		v, e := Compile(raw)
		if e != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	})
	return m
}
func ServeEditor(ctx context.Context) error {
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return e
	}
	b := make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		ln.Close()
		return e
	}
	token := hex.EncodeToString(b)
	s := &http.Server{Handler: editorHandler(token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	fmt.Printf("Open locally: http://%s/#%s\nCtrl+C stops this editor. No settings are saved automatically.\n", ln.Addr(), token)
	e = s.Serve(ln)
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}

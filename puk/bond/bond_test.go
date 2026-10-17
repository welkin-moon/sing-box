package bond

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listener(t *testing.T) net.Listener {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func target(t *testing.T, fn func(net.Conn)) string {
	l := listener(t)
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); fn(c) }()
		}
	}()
	return l.Addr().String()
}
func hubPipe(h *Hub) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) { a, b := net.Pipe(); go h.ServeConn(b); return a, nil }
}
func key() string { return strings.Repeat("12", 32) }
func TestDualPathsChainsAndHalfClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dials atomic.Int32
	destination := target(t, func(c net.Conn) {
		b, e := io.ReadAll(c)
		if e != nil {
			return
		}
		sum := sha256.Sum256(b)
		writeAll(c, sum[:])
		writeAll(c, b)
	})
	h := &Hub{ID: "jp", Users: map[string]string{"u": key()}, Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}}
	defer h.Close()
	ws := httptest.NewTLSServer(h.WebSocketHandler("/bond"))
	defer ws.Close()
	sum := sha256.Sum256(ws.Certificate().Raw)
	pin := hex.EncodeToString(sum[:])
	raw := tls.NewListener(listener(t), &tls.Config{Certificates: ws.TLS.Certificates, MinVersion: tls.VersionTLS12})
	go func() {
		for {
			c, e := raw.Accept()
			if e != nil {
				return
			}
			go h.ServeConn(c)
		}
	}()
	var first, second atomic.Int32
	one, two := listener(t), listener(t)
	go ServeSOCKS(ctx, one, func(ctx context.Context, n, a string) (net.Conn, error) {
		first.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, a)
	})
	go ServeSOCKS(ctx, two, func(ctx context.Context, n, a string) (net.Conn, error) {
		second.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, a)
	})
	up := Path{ID: "tw-jp", Hub: "jp", URL: "wss" + strings.TrimPrefix(ws.URL, "https") + "/bond", CertificateSHA256: pin, Chain: []Hop{{Kind: "socks5", Address: one.Addr().String()}, {Kind: "socks5", Address: two.Addr().String()}}}
	down := Path{ID: "jp-direct", Hub: "jp", URL: "tls://" + raw.Addr().String(), CertificateSHA256: pin}
	client := Client{Server: "jp", User: "u", Secret: key(), Upload: up.Dial(nil), Download: down.Dial(nil)}
	conn, e := client.Open(ctx, destination)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	payload := bytes.Repeat([]byte("0123456789abcdef"), 512*1024)
	if _, e = conn.Write(payload); e != nil {
		t.Fatal(e)
	}
	if e = conn.(interface{ CloseWrite() error }).CloseWrite(); e != nil {
		t.Fatal(e)
	}
	result, e := io.ReadAll(conn)
	if e != nil {
		t.Fatal(e)
	}
	sum = sha256.Sum256(payload)
	if !bytes.Equal(result[:32], sum[:]) || !bytes.Equal(result[32:], payload) {
		t.Fatal("upload/download corruption")
	}
	if dials.Load() != 1 || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("paths did not share exactly one website connection: %d,%d,%d", dials.Load(), first.Load(), second.Load())
	}
}
func TestConcurrentSessionsAuthAndCancel(t *testing.T) {
	destination := target(t, func(c net.Conn) { io.Copy(c, c) })
	h := &Hub{ID: "hub", Users: map[string]string{"u": key()}, PairTimeout: 100 * time.Millisecond}
	defer h.Close()
	fn := hubPipe(h)
	good := Client{Server: "hub", User: "u", Secret: key(), Upload: fn, Download: fn}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, e := good.Open(ctx, destination)
			if e != nil {
				t.Error(e)
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			data := bytes.Repeat([]byte("abcdef"), 1024)
			if _, e = c.Write(data); e != nil {
				t.Error(e)
				return
			}
			got := make([]byte, len(data))
			if _, e = io.ReadFull(c, got); e != nil || !bytes.Equal(data, got) {
				t.Error("session isolation/integrity", e)
			}
		}()
	}
	wg.Wait()
	bad := good
	bad.Secret = strings.Repeat("34", 32)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, e := bad.Open(ctx, destination); e == nil {
		c.Close()
		t.Fatal("wrong credentials accepted")
	}
	bad = good
	bad.Server = "another hub"
	if c, e := bad.Open(ctx, destination); e == nil {
		c.Close()
		t.Fatal("wrong hub accepted")
	}
	bad = good
	bad.Download = func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	short, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if c, e := bad.Open(short, destination); e == nil {
		c.Close()
		t.Fatal("missing leg accepted")
	}
}
func TestReplayAndPairTimeout(t *testing.T) {
	h := &Hub{ID: "hub", Users: map[string]string{"u": key()}, PairTimeout: 30 * time.Millisecond}
	defer h.Close()
	sid, _ := randomID()
	key, _ := keyBytes(key())
	a := hello{Version: 1, Server: "hub", User: "u", Session: sid, Role: "up", Target: "example.org:443", At: time.Now().Unix()}
	a.MAC = a.signed(key)
	raw, _ := json.Marshal(a)
	connect := func() net.Conn {
		c, s := net.Pipe()
		go h.ServeConn(s)
		c.SetDeadline(time.Now().Add(time.Second))
		if e := writePacket(c, raw); e != nil {
			t.Fatal(e)
		}
		return c
	}
	c := connect()
	defer c.Close()
	if _, e := readPacket(c, 256); e != nil {
		t.Fatal(e)
	}
	duplicate := connect()
	defer duplicate.Close()
	if _, e := readPacket(duplicate, 256); e == nil {
		t.Fatal("duplicate upload role accepted")
	}
	var b [1]byte
	if _, e := c.Read(b[:]); e == nil {
		t.Fatal("orphan pair not reaped")
	}
	replayed := connect()
	defer replayed.Close()
	if _, e := readPacket(replayed, 256); e == nil {
		t.Fatal("spent session replay accepted")
	}
}
func TestBrokenLegClosesWebsite(t *testing.T) {
	destination := target(t, func(c net.Conn) { io.Copy(c, c) })
	h := &Hub{ID: "hub", Users: map[string]string{"u": key()}}
	defer h.Close()
	c, e := (Client{Server: "hub", User: "u", Secret: key(), Upload: hubPipe(h), Download: hubPipe(h)}).Open(context.Background(), destination)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.(*Conn).Up.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("broken upload silently continued")
	}
}
func TestLibraryAndEditorBoundaries(t *testing.T) {
	l, e := Load(ExampleLibrary)
	if e != nil {
		t.Fatal(e)
	}
	config, e := l.NativeConfig()
	if e != nil {
		t.Fatal(e)
	}
	outs := config["outbounds"].([]any)
	if len(outs) != 4 || outs[2].(map[string]any)["detour"] != "tw" {
		t.Fatal("imported chain discarded")
	}
	if _, ok := l.Core["inbounds"]; ok {
		t.Fatal("export mutated source")
	}
	l.Profiles[0].Hub = "other"
	if e = l.Validate(); e == nil {
		t.Fatal("different hub accepted")
	}
	l, _ = Load(ExampleLibrary)
	l.Profiles[0].URL = "tcp://192.0.2.1:8080"
	if e = l.Validate(); e == nil {
		t.Fatal("public plaintext accepted")
	}
	m := editorHandler("token")
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/compile", bytes.NewReader(ExampleLibrary))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("unauthorized editor request")
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1/api/compile", bytes.NewReader(ExampleLibrary))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PUK-Token", "token")
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-origin editor request")
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1/api/compile", bytes.NewReader(ExampleLibrary))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PUK-Token", "token")
	w = httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCoreChainCycleRejected(t *testing.T) {
	l, e := Load(ExampleLibrary)
	if e != nil {
		t.Fatal(e)
	}
	outs := l.Core["outbounds"].([]any)
	outs[1].(map[string]any)["detour"] = "jp-via-tw"
	if e = l.Validate(); e == nil {
		t.Fatal("cyclic chain accepted")
	}
}

func TestWSKeepaliveWithoutApplicationReads(t *testing.T) {
	done := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		stream := newWSStreamWith(c, 20*time.Millisecond, 200*time.Millisecond)
		defer stream.Close()
		defer close(done)
		b := make([]byte, 1)
		if _, e = io.ReadFull(stream, b); e == nil {
			stream.Write(b)
		}
	}))
	defer s.Close()
	raw, _, e := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	c := newWSStreamWith(raw, 20*time.Millisecond, 200*time.Millisecond)
	defer c.Close()
	time.Sleep(120 * time.Millisecond)
	c.SetDeadline(time.Now().Add(time.Second))
	if _, e = c.Write([]byte{42}); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 1)
	if _, e = io.ReadFull(c, b); e != nil || b[0] != 42 {
		t.Fatal("idle leg failed without app reader", e)
	}
	<-done
}

func TestWSBackpressureDoesNotInvalidateSession(t *testing.T) {
	payload := bytes.Repeat([]byte{17}, 128*1024)
	done := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		stream := newWSStreamWith(c, 10*time.Millisecond, 10*time.Millisecond)
		defer stream.Close()
		defer close(done)
		for offset := 0; offset < len(payload); offset += 16384 {
			if _, e = stream.Write(payload[offset : offset+16384]); e != nil {
				return
			}
		}
		io.ReadFull(stream, make([]byte, 1))
	}))
	defer s.Close()
	raw, _, e := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	c := newWSStreamWith(raw, 10*time.Millisecond, 10*time.Millisecond)
	defer c.Close()
	time.Sleep(150 * time.Millisecond)
	c.SetDeadline(time.Now().Add(time.Second))
	b := make([]byte, len(payload))
	if _, e = io.ReadFull(c, b); e != nil || !bytes.Equal(b, payload) {
		t.Fatal("backpressure lost payload", e)
	}
	c.Write([]byte{1})
	<-done
}

func TestIndependentServerListeners(t *testing.T) {
	l, e := Load(ExampleLibrary)
	if e != nil {
		t.Fatal(e)
	}
	s := l.ServerTemplate()
	s.Listeners = append(s.Listeners, Listener{Kind: "tls", Listen: "0.0.0.0:18443", Certificate: "/etc/puk-bond/tls.crt", Key: "/etc/puk-bond/tls.key"})
	if e = s.Validate(); e != nil {
		t.Fatal(e)
	}
	s.Listeners[1].Listen = "0.0.0.0:18086"
	if e = s.Validate(); e == nil {
		t.Fatal("public plaintext WS listener accepted")
	}
}

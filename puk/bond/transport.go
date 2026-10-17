//go:build !js

package bond

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func socksConnect(c net.Conn, h Hop, target string) error {
	methods := []byte{5, 1, 0}
	if h.Username != "" {
		methods = []byte{5, 1, 2}
	}
	if e := writeAll(c, methods); e != nil {
		return e
	}
	var reply [2]byte
	if _, e := io.ReadFull(c, reply[:]); e != nil {
		return e
	}
	if reply[0] != 5 {
		return errors.New("invalid SOCKS bridge")
	}
	if reply[1] == 2 && h.Username != "" {
		b := []byte{1, byte(len(h.Username))}
		b = append(b, h.Username...)
		b = append(b, byte(len(h.Password)))
		b = append(b, h.Password...)
		if e := writeAll(c, b); e != nil {
			return e
		}
		if _, e := io.ReadFull(c, reply[:]); e != nil {
			return e
		}
		if reply[0] != 1 || reply[1] != 0 {
			return errors.New("SOCKS authentication failed")
		}
	} else if reply[1] != 0 || h.Username != "" {
		return errors.New("SOCKS method rejected")
	}
	host, ps, e := net.SplitHostPort(target)
	if e != nil {
		return e
	}
	port, e := strconv.Atoi(ps)
	if e != nil {
		return e
	}
	b := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			b = append(b, 1)
			b = append(b, v4...)
		} else {
			b = append(b, 4)
			b = append(b, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("host too long")
		}
		b = append(b, 3, byte(len(host)))
		b = append(b, host...)
	}
	b = append(b, byte(port>>8), byte(port))
	if e = writeAll(c, b); e != nil {
		return e
	}
	var header [4]byte
	if _, e = io.ReadFull(c, header[:]); e != nil {
		return e
	}
	if header[0] != 5 || header[1] != 0 {
		return errors.New("SOCKS CONNECT failed")
	}
	var n int
	switch header[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var size [1]byte
		if _, e = io.ReadFull(c, size[:]); e != nil {
			return e
		}
		n = int(size[0])
	default:
		return errors.New("invalid SOCKS address")
	}
	_, e = io.CopyN(io.Discard, c, int64(n+2))
	return e
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// ChainDialer is also usable on the hub for an independent server egress chain.
func ChainDialer(base DialFunc, hops []Hop) DialFunc {
	if base == nil {
		base = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" {
			return nil, errors.New("bridge adapter only supports TCP streams")
		}
		first := target
		if len(hops) > 0 {
			first = hops[0].Address
		}
		c, e := base(ctx, "tcp", first)
		if e != nil {
			return nil, e
		}
		originalConn := c
		stop := context.AfterFunc(ctx, func() { originalConn.Close() })
		defer stop()
		deadline := time.Now().Add(15 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		c.SetDeadline(deadline)
		for i, h := range hops {
			next := target
			if i+1 < len(hops) {
				next = hops[i+1].Address
			}
			switch h.Kind {
			case "socks5":
				e = socksConnect(c, h, next)
			case "http":
				r := &http.Request{Method: "CONNECT", URL: &url.URL{Opaque: next}, Host: next, Header: http.Header{}}
				if h.Username != "" {
					r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(h.Username+":"+h.Password)))
				}
				e = r.Write(c)
				if e == nil {
					br := bufio.NewReaderSize(c, 8192)
					var response *http.Response
					response, e = http.ReadResponse(br, r)
					if e == nil {
						if response.StatusCode != 200 {
							e = errors.New("HTTP CONNECT rejected")
						} else {
							c = &bufferedConn{c, br}
						}
					}
				}
			default:
				e = errors.New("unsupported bridge")
			}
			if e != nil {
				c.Close()
				return nil, e
			}
		}
		c.SetDeadline(time.Time{})
		return c, nil
	}
}
func (p Path) tlsConfig(host string) *tls.Config {
	sni := p.ServerName
	if sni == "" {
		sni = host
	}
	cfg := &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12}
	if p.CertificateSHA256 != "" {
		pin, _ := hex.DecodeString(p.CertificateSHA256)
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("missing peer certificate")
			}
			leaf := cs.PeerCertificates[0]
			sum := sha256.Sum256(leaf.Raw)
			if !equalBytes(sum[:], pin) {
				return errors.New("server certificate pin mismatch")
			}
			now := time.Now()
			if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
				return errors.New("server certificate expired or not yet valid")
			}
			if e := leaf.VerifyHostname(sni); e != nil {
				return e
			}
			return nil
		}
	}
	return cfg
}
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
func (p Path) Dial(base DialFunc) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		if e := p.Validate(); e != nil {
			return nil, e
		}
		u, _ := url.Parse(p.URL)
		dial := ChainDialer(base, p.Chain)
		if u.Scheme == "tcp" || u.Scheme == "tls" {
			c, e := dial(ctx, "tcp", u.Host)
			if e != nil {
				return nil, e
			}
			if u.Scheme == "tls" {
				t := tls.Client(c, p.tlsConfig(u.Hostname()))
				if e = t.HandshakeContext(ctx); e != nil {
					c.Close()
					return nil, e
				}
				c = t
			}
			return c, nil
		}
		transport := &http.Transport{DialContext: dial, TLSClientConfig: p.tlsConfig(u.Hostname()), ForceAttemptHTTP2: false}
		c, resp, e := websocket.Dial(ctx, p.URL, &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, CompressionMode: websocket.CompressionDisabled})
		if e != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			transport.CloseIdleConnections()
			return nil, e
		}
		return newWSStream(c), nil
	}
}

type wsChunk struct {
	data []byte
	err  error
}
type wsStream struct {
	net.Conn
	c       *websocket.Conn
	cancel  context.CancelFunc
	once    sync.Once
	chunks  chan wsChunk
	ctx     context.Context
	rmu     sync.Mutex
	pending []byte
	readErr error
	busy    atomic.Bool
	writing atomic.Int32
	last    atomic.Int64
}

func newWSStream(c *websocket.Conn) net.Conn {
	return newWSStreamWith(c, 25*time.Second, 8*time.Second)
}
func newWSStreamWith(c *websocket.Conn, interval, timeout time.Duration) net.Conn {
	ctx, cancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
	c.SetReadLimit(2 * maxFrame)
	w := &wsStream{Conn: nc, c: c, cancel: cancel, ctx: ctx, chunks: make(chan wsChunk, 1)}
	w.last.Store(time.Now().UnixNano())
	// Always process WS control traffic, even while the application only writes.
	go func() {
		defer close(w.chunks)
		for {
			b := make([]byte, 16384)
			n, e := nc.Read(b)
			if n > 0 {
				w.last.Store(time.Now().UnixNano())
			}
			if n > 0 || e != nil {
				w.busy.Store(true)
				select {
				case w.chunks <- wsChunk{b[:n], e}:
					w.busy.Store(false)
				case <-ctx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// Backpressure is activity, not a failed keepalive. Do not time out a blocked writer/reader.
				if w.busy.Load() || w.writing.Load() > 0 || time.Since(time.Unix(0, w.last.Load())) < interval {
					continue
				}
				pctx, stop := context.WithTimeout(ctx, timeout)
				e := c.Ping(pctx)
				stop()
				if e != nil && !errors.Is(e, context.DeadlineExceeded) {
					w.Close()
					return
				}
			}
		}
	}()
	return w
}
func (w *wsStream) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	w.rmu.Lock()
	defer w.rmu.Unlock()
	for len(w.pending) == 0 {
		if w.readErr != nil {
			return 0, w.readErr
		}
		chunk, ok := <-w.chunks
		if !ok {
			return 0, io.EOF
		}
		w.pending = chunk.data
		w.readErr = chunk.err
	}
	n := copy(b, w.pending)
	w.pending = w.pending[n:]
	return n, nil
}
func (w *wsStream) Write(b []byte) (int, error) {
	w.writing.Add(1)
	defer w.writing.Add(-1)
	n, e := w.Conn.Write(b)
	if n > 0 {
		w.last.Store(time.Now().UnixNano())
	}
	return n, e
}
func (w *wsStream) Close() error { w.once.Do(func() { w.cancel(); w.c.CloseNow() }); return nil }

// WebSocketHandler only adapts the carrier; it shares the same Hub as TCP.
func (h *Hub) WebSocketHandler(path string) http.Handler { return h.WebSocketHandlers([]string{path}) }
func (h *Hub) WebSocketHandlers(paths []string) http.Handler {
	m := http.NewServeMux()
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		m.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != path {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Origin") != "" {
				http.Error(w, "browser origins are not allowed", 403)
				return
			}
			c, e := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
			if e != nil {
				return
			}
			h.ServeConn(newWSStream(c))
		})
	}
	return m
}

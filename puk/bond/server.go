package bond

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type DialFunc func(context.Context, string, string) (net.Conn, error)
type pair struct {
	closed                 bool
	h                      hello
	up, down               net.Conn
	claimedUp, claimedDown bool
	ready                  chan struct{}
	once                   sync.Once
	timer                  *time.Timer
	target                 net.Conn
}

// Hub must be shared by all listeners/CF adapters for the same exit.
// Running two independent Hubs behind the same IP does not share sessions.
type Hub struct {
	pending     atomic.Int32
	ID          string
	Users       map[string]string // immutable after first use; user -> 32-byte hexadecimal secret
	Dial        DialFunc          // optional server egress chain; nil uses the device's ordinary network
	MaxSessions int
	PairTimeout time.Duration
	IdleTimeout time.Duration
	mu          sync.Mutex
	sessions    map[string]*pair
	spent       map[string]int64
	closed      bool
}

func (h *Hub) closePair(key string, p *pair) {
	p.once.Do(func() {
		h.mu.Lock()
		p.closed = true
		delete(h.sessions, key)
		up, down, target := p.up, p.down, p.target
		h.mu.Unlock()
		if up != nil {
			up.Close()
		}
		if down != nil {
			down.Close()
		}
		if target != nil {
			target.Close()
		}
		close(p.ready)
	})
}
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	pairs := map[string]*pair{}
	for k, v := range h.sessions {
		pairs[k] = v
	}
	h.mu.Unlock()
	for k, p := range pairs {
		h.closePair(k, p)
	}
}
func (h *Hub) ServeConn(c net.Conn) {
	if h.pending.Add(1) > 64 {
		h.pending.Add(-1)
		c.Close()
		return
	}
	defer h.pending.Add(-1)
	c.SetDeadline(time.Now().Add(10 * time.Second))
	raw, e := readPacket(c, 8192)
	if e != nil {
		c.Close()
		return
	}
	var a hello
	if json.Unmarshal(raw, &a) != nil {
		c.Close()
		return
	}
	secret, ok := h.Users[a.User]
	key, e := keyBytes(secret)
	sig, err := hex.DecodeString(a.MAC)
	want, _ := hex.DecodeString(a.signed(key))
	sid, serr := hex.DecodeString(a.Session)
	now := time.Now().Unix()
	if !ok || e != nil || err != nil || serr != nil || len(sid) != 32 || !hmac.Equal(sig, want) || a.Version != 1 || a.Server != h.ID || (a.Role != "up" && a.Role != "down") || a.At < now-60 || a.At > now+60 || !validTarget(a.Target) {
		c.Close()
		return
	}
	id := a.User + ":" + a.Session
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		c.Close()
		return
	}
	if h.sessions == nil {
		h.sessions = map[string]*pair{}
		h.spent = map[string]int64{}
	}
	for k, t := range h.spent {
		if t < now {
			delete(h.spent, k)
		}
	}
	p, exists := h.sessions[id]
	limit := h.MaxSessions
	if limit <= 0 {
		limit = 128
	}
	if !exists {
		if _, replay := h.spent[id]; replay || len(h.sessions) >= limit || len(h.spent) >= limit*8 {
			h.mu.Unlock()
			c.Close()
			return
		}
		p = &pair{h: a, ready: make(chan struct{})}
		h.sessions[id] = p
		h.spent[id] = a.At + 120
		timeout := h.PairTimeout
		if timeout == 0 {
			timeout = 15 * time.Second
		}
		p.timer = time.AfterFunc(timeout, func() { h.closePair(id, p) })
	}
	// Prevent mixing target, timestamp, user or role under a reused session ID.
	if p.h.Target != a.Target || p.h.At != a.At || a.Role == "up" && p.claimedUp || a.Role == "down" && p.claimedDown {
		h.mu.Unlock()
		c.Close()
		return
	}
	if a.Role == "up" {
		p.claimedUp = true
	} else {
		p.claimedDown = true
	}
	h.mu.Unlock()
	ack := digest(key, a.MAC+"/accepted/"+h.ID)
	if writePacket(c, []byte(ack)) != nil {
		c.Close()
		h.closePair(id, p)
		return
	}
	c.SetDeadline(time.Time{})
	h.mu.Lock()
	if p.closed {
		h.mu.Unlock()
		c.Close()
		return
	}
	if a.Role == "up" {
		p.up = c
	} else {
		p.down = c
	}
	start := p.up != nil && p.down != nil
	if start {
		p.timer.Stop()
	}
	h.mu.Unlock()
	if a.Role == "down" {
		go func() { var b [1]byte; c.Read(b[:]); h.closePair(id, p) }()
	}
	if start {
		go h.relay(id, p)
	}
	// All stream I/O belongs to relay; the WS handler may return after takeover.
}
func (h *Hub) relay(id string, p *pair) {
	defer h.closePair(id, p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := h.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	target, e := dial(ctx, "tcp", p.h.Target)
	if e != nil {
		writeFrame(p.down, errorFrame, nil)
		return
	}
	h.mu.Lock()
	if p.closed {
		h.mu.Unlock()
		target.Close()
		return
	}
	p.target = target
	h.mu.Unlock()
	if writeFrame(p.down, readyFrame, nil) != nil {
		return
	}
	idle := h.IdleTimeout
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			p.up.SetReadDeadline(time.Now().Add(idle))
			t, b, e := readFrame(p.up)
			if e != nil {
				target.Close()
				return
			}
			switch t {
			case dataFrame:
				target.SetWriteDeadline(time.Now().Add(idle))
				if writeAll(target, b) != nil {
					target.Close()
					return
				}
			case finFrame:
				if len(b) != 0 {
					target.Close()
					return
				}
				if cw, ok := target.(interface{ CloseWrite() error }); ok {
					cw.CloseWrite()
				} else {
					target.Close()
				}
				return
			default:
				target.Close()
				return
			}
		}
	}()
	cleanEOF := false
	buf := make([]byte, maxFrame-1)
	for {
		target.SetReadDeadline(time.Now().Add(idle))
		n, e := target.Read(buf)
		if n > 0 {
			p.down.SetWriteDeadline(time.Now().Add(idle))
			if writeFrame(p.down, dataFrame, buf[:n]) != nil {
				break
			}
		}
		if e != nil {
			if errors.Is(e, io.EOF) {
				cleanEOF = writeFrame(p.down, finFrame, nil) == nil
			} else {
				writeFrame(p.down, errorFrame, nil)
			}
			break
		}
	}
	// Tear down the upload reader too. Never keep an orphaned goroutine on a broken leg.
	if !cleanEOF {
		p.up.Close()
		target.Close()
	}
	<-done
}

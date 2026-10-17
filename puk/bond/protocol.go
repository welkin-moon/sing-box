// Package bond joins independently dialed upload/download legs at one authenticated hub.
// It is independent of sing-box, Xray, XHTTP, and the path taken by either leg.
package bond

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const maxFrame = 64 * 1024
const (
	dataFrame  byte = 1
	finFrame   byte = 2
	closeFrame byte = 3
	readyFrame byte = 4
	errorFrame byte = 5
)

type hello struct {
	Version int    `json:"version"`
	Server  string `json:"server"`
	User    string `json:"user"`
	Session string `json:"session"`
	Role    string `json:"role"`
	Target  string `json:"target"`
	At      int64  `json:"at"`
	MAC     string `json:"mac"`
}

func digest(key []byte, s string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}
func (h hello) signed(key []byte) string {
	h.MAC = ""
	b, _ := json.Marshal(h)
	return digest(key, string(b))
}
func keyBytes(s string) ([]byte, error) {
	b, e := hex.DecodeString(s)
	if e != nil || len(b) != 32 {
		return nil, errors.New("secret must be 32 random bytes encoded as 64 hex characters")
	}
	return b, nil
}
func randomID() (string, error) {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func writePacket(w io.Writer, b []byte) error {
	if len(b) > maxFrame {
		return errors.New("packet too large")
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	if e := writeAll(w, n[:]); e != nil {
		return e
	}
	return writeAll(w, b)
}
func readPacket(r io.Reader, max uint32) ([]byte, error) {
	var n [4]byte
	if _, e := io.ReadFull(r, n[:]); e != nil {
		return nil, e
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > max {
		return nil, errors.New("packet too large")
	}
	b := make([]byte, size)
	_, e := io.ReadFull(r, b)
	return b, e
}
func writeFrame(w io.Writer, t byte, b []byte) error {
	if len(b) > maxFrame-1 {
		return errors.New("frame too large")
	}
	return writePacket(w, append([]byte{t}, b...))
}
func readFrame(r io.Reader) (byte, []byte, error) {
	b, e := readPacket(r, maxFrame)
	if e != nil {
		return 0, nil, e
	}
	if len(b) == 0 {
		return 0, nil, errors.New("empty frame")
	}
	return b[0], b[1:], nil
}
func validTarget(s string) bool {
	host, port, e := net.SplitHostPort(s)
	if e != nil || host == "" {
		return false
	}
	n, e := net.LookupPort("tcp", port)
	return e == nil && n > 0 && n <= 65535
}

// Conn reads payload only from Down and writes payload only to Up.
// Handshakes and carrier control messages are necessarily bidirectional.
type Conn struct {
	Up, Down         net.Conn
	rmu, wmu         sync.Mutex
	pending          []byte
	eof, writeClosed bool
	once             sync.Once
}

func (c *Conn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for len(c.pending) == 0 {
		if c.eof {
			return 0, io.EOF
		}
		t, p, e := readFrame(c.Down)
		if e != nil {
			return 0, e
		}
		switch t {
		case dataFrame:
			c.pending = p
		case finFrame:
			c.eof = true
		case errorFrame:
			return 0, errors.New("remote connection failed")
		default:
			return 0, fmt.Errorf("unexpected downlink frame %d", t)
		}
	}
	n := copy(b, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func (c *Conn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.writeClosed {
		return 0, net.ErrClosed
	}
	n := 0
	for len(b) > 0 {
		size := len(b)
		if size > maxFrame-1 {
			size = maxFrame - 1
		}
		if e := writeFrame(c.Up, dataFrame, b[:size]); e != nil {
			return n, e
		}
		n += size
		b = b[size:]
	}
	return n, nil
}
func (c *Conn) CloseWrite() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.writeClosed {
		return nil
	}
	c.writeClosed = true
	return writeFrame(c.Up, finFrame, nil)
}
func (c *Conn) Close() error         { c.once.Do(func() { c.Up.Close(); c.Down.Close() }); return nil }
func (c *Conn) LocalAddr() net.Addr  { return c.Up.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.Down.RemoteAddr() }
func (c *Conn) SetDeadline(t time.Time) error {
	if e := c.Up.SetDeadline(t); e != nil {
		return e
	}
	return c.Down.SetDeadline(t)
}
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.Down.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.Up.SetWriteDeadline(t) }

func ValidSecret(s string) bool  { _, e := keyBytes(s); return e == nil }
func NewSecret() (string, error) { return randomID() }

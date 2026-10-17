package bond

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net"
	"time"
)

type Client struct {
	Server, User, Secret string
	Upload, Download     func(context.Context) (net.Conn, error)
}

func (c Client) Open(ctx context.Context, target string) (net.Conn, error) {
	if !validTarget(target) {
		return nil, errors.New("invalid target")
	}
	key, e := keyBytes(c.Secret)
	if e != nil {
		return nil, e
	}
	if c.Upload == nil || c.Download == nil {
		return nil, errors.New("choose both paths")
	}
	sid, e := randomID()
	if e != nil {
		return nil, e
	}
	a := hello{Version: 1, Server: c.Server, User: c.User, Session: sid, Target: target, At: time.Now().Unix()}
	type leg struct {
		role string
		conn net.Conn
		err  error
	}
	results := make(chan leg, 2)
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for role, fn := range map[string]func(context.Context) (net.Conn, error){"up": c.Upload, "down": c.Download} {
		go func(role string, fn func(context.Context) (net.Conn, error)) {
			conn, e := fn(work)
			if e == nil {
				originalConn := conn
				stop := context.AfterFunc(work, func() { originalConn.Close() })
				conn.SetDeadline(time.Now().Add(20 * time.Second))
				h := a
				h.Role = role
				h.MAC = h.signed(key)
				raw, _ := json.Marshal(h)
				e = writePacket(conn, raw)
				if e == nil {
					var ack []byte
					ack, e = readPacket(conn, 256)
					if e == nil && !hmac.Equal(ack, []byte(digest(key, h.MAC+"/accepted/"+c.Server))) {
						e = errors.New("hub authentication failed")
					}
				}
				stop()
			}
			if e != nil && conn != nil {
				conn.Close()
				conn = nil
			}
			results <- leg{role, conn, e}
		}(role, fn)
	}
	var up, down net.Conn
	var first error
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			if first == nil {
				first = r.err
			}
			cancel()
		} else if r.role == "up" {
			up = r.conn
		} else {
			down = r.conn
		}
	}
	if first != nil {
		if up != nil {
			up.Close()
		}
		if down != nil {
			down.Close()
		}
		return nil, first
	}
	stop := context.AfterFunc(work, func() { up.Close(); down.Close() })
	t, _, e := readFrame(down)
	stop()
	if e != nil || t != readyFrame {
		up.Close()
		down.Close()
		return nil, errors.New("hub could not open target or pair paths")
	}
	up.SetDeadline(time.Time{})
	down.SetDeadline(time.Time{})
	go func() { var b [1]byte; up.Read(b[:]); up.Close() }()
	return &Conn{Up: up, Down: down}, nil
}

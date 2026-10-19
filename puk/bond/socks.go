package bond

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// ServeSOCKS is a loopback-only TCP front end. UDP ASSOCIATE is explicitly rejected.
func ServeSOCKS(ctx context.Context, ln net.Listener, dial DialFunc) error {
	if !loopback(ln.Addr().String()) {
		return fmt.Errorf("SOCKS listener must be loopback")
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	limit := make(chan struct{}, 128)
	for {
		c, e := ln.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		select {
		case limit <- struct{}{}:
		default:
			c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			defer c.Close()
			done := context.AfterFunc(ctx, func() { c.Close() })
			defer done()
			c.SetDeadline(time.Now().Add(15 * time.Second))
			var h [2]byte
			if _, e := io.ReadFull(c, h[:]); e != nil || h[0] != 5 {
				return
			}
			methods := make([]byte, int(h[1]))
			if _, e := io.ReadFull(c, methods); e != nil {
				return
			}
			ok := false
			for _, m := range methods {
				if m == 0 {
					ok = true
				}
			}
			if !ok {
				c.Write([]byte{5, 255})
				return
			}
			if writeAll(c, []byte{5, 0}) != nil {
				return
			}
			var header [4]byte
			if _, e := io.ReadFull(c, header[:]); e != nil || header[0] != 5 {
				return
			}
			if header[1] != 1 {
				c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
				return
			}
			host := ""
			switch header[3] {
			case 1:
				b := make([]byte, 4)
				if _, e := io.ReadFull(c, b); e != nil {
					return
				}
				host = net.IP(b).String()
			case 4:
				b := make([]byte, 16)
				if _, e := io.ReadFull(c, b); e != nil {
					return
				}
				host = net.IP(b).String()
			case 3:
				var n [1]byte
				if _, e := io.ReadFull(c, n[:]); e != nil {
					return
				}
				b := make([]byte, n[0])
				if _, e := io.ReadFull(c, b); e != nil {
					return
				}
				host = string(b)
			default:
				return
			}
			var p [2]byte
			if _, e := io.ReadFull(c, p[:]); e != nil {
				return
			}
			address := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:]))))
			work, cancel := context.WithTimeout(ctx, 20*time.Second)
			remote, e := dial(work, "tcp", address)
			cancel()
			if e != nil {
				c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
				return
			}
			defer remote.Close()
			rstop := context.AfterFunc(ctx, func() { remote.Close() })
			defer rstop()
			if writeAll(c, []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}) != nil {
				return
			}
			c.SetDeadline(time.Time{})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				io.Copy(remote, c)
				if cw, ok := remote.(interface{ CloseWrite() error }); ok {
					cw.CloseWrite()
				} else {
					remote.Close()
				}
			}()
			io.Copy(c, remote)
			c.Close()
			remote.Close()
			<-finished
		}()
	}
}

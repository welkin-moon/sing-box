//go:build !js

package bond

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

func (s ServerConfig) Run(ctx context.Context) error {
	if e := s.Validate(); e != nil {
		return e
	}
	h := &Hub{ID: s.ID, Users: s.Users, MaxSessions: s.MaxSessions, Dial: ChainDialer(nil, s.ExitChain)}
	defer h.Close()
	var tlsConfig *tls.Config
	if s.Certificate != "" {
		cert, e := tls.LoadX509KeyPair(s.Certificate, s.Key)
		if e != nil {
			return e
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for _, a := range []string{s.Listen, s.WSListen} {
		if a == "" {
			continue
		}
		ln, e := net.Listen("tcp", a)
		if e != nil {
			return e
		}
		if tlsConfig != nil {
			ln = tls.NewListener(ln, tlsConfig)
		}
		listeners = append(listeners, ln)
	}
	errs := make(chan error, 2)
	i := 0
	var hs *http.Server
	if s.Listen != "" {
		ln := listeners[i]
		i++
		go func() {
			slots := make(chan struct{}, 64)
			for {
				c, e := ln.Accept()
				if e != nil {
					errs <- e
					return
				}
				select {
				case slots <- struct{}{}:
					go func() { defer func() { <-slots }(); h.ServeConn(c) }()
				default:
					c.Close()
				}
			}
		}()
	}
	if s.WSListen != "" {
		hs = &http.Server{Handler: h.WebSocketHandlers(append([]string{s.WSPath}, s.WSPaths...)), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16384}
		ln := listeners[i]
		go func() { errs <- hs.Serve(ln) }()
	}
	select {
	case <-ctx.Done():
		if hs != nil {
			hs.Close()
		}
		return nil
	case e := <-errs:
		if hs != nil {
			hs.Close()
		}
		return e
	}
}

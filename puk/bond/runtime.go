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
	options := s.listeners()
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	for _, l := range options {
		var tc *tls.Config
		if l.Kind == "tls" || l.Kind == "wss" {
			cert, e := tls.LoadX509KeyPair(l.Certificate, l.Key)
			if e != nil {
				return e
			}
			tc = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		}
		ln, e := net.Listen("tcp", l.Listen)
		if e != nil {
			return e
		}
		if tc != nil {
			ln = tls.NewListener(ln, tc)
		}
		listeners = append(listeners, ln)
	}
	errs := make(chan error, len(options))
	var servers []*http.Server
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()
	for i, l := range options {
		ln := listeners[i]
		if l.Kind == "ws" || l.Kind == "wss" {
			server := &http.Server{Handler: h.WebSocketHandlers(append([]string{l.Path}, l.Paths...)), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16384}
			servers = append(servers, server)
			go func() { errs <- server.Serve(ln) }()
		} else {
			go func() {
				for {
					c, e := ln.Accept()
					if e != nil {
						errs <- e
						return
					}
					go h.ServeConn(c)
				}
			}()
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case e := <-errs:
		return e
	}
}

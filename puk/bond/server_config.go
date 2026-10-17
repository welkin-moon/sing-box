package bond

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

type Listener struct {
	Kind        string   `json:"kind"`
	Listen      string   `json:"listen"`
	Path        string   `json:"path,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Certificate string   `json:"certificate,omitempty"`
	Key         string   `json:"key,omitempty"`
}
type ServerConfig struct {
	ID        string            `json:"id"`
	Users     map[string]string `json:"users"`
	Listeners []Listener        `json:"listeners,omitempty"`
	// v1 CLI compatibility; new templates use independent listener modules.
	Listen      string   `json:"listen,omitempty"`
	WSListen    string   `json:"ws_listen,omitempty"`
	WSPath      string   `json:"ws_path,omitempty"`
	WSPaths     []string `json:"ws_paths,omitempty"`
	Certificate string   `json:"certificate,omitempty"`
	Key         string   `json:"key,omitempty"`
	ExitChain   []Hop    `json:"exit_chain,omitempty"`
	MaxSessions int      `json:"max_sessions,omitempty"`
}

func (s ServerConfig) listeners() []Listener {
	if len(s.Listeners) > 0 {
		return s.Listeners
	}
	var a []Listener
	kind := "tcp"
	if s.Certificate != "" {
		kind = "tls"
	}
	if s.Listen != "" {
		a = append(a, Listener{Kind: kind, Listen: s.Listen, Certificate: s.Certificate, Key: s.Key})
	}
	kind = "ws"
	if s.Certificate != "" {
		kind = "wss"
	}
	if s.WSListen != "" {
		a = append(a, Listener{Kind: kind, Listen: s.WSListen, Path: s.WSPath, Paths: s.WSPaths, Certificate: s.Certificate, Key: s.Key})
	}
	return a
}
func (s ServerConfig) Validate() error {
	if s.ID == "" || len(s.Users) == 0 {
		return errors.New("hub ID and users required")
	}
	for u, key := range s.Users {
		if u == "" || !ValidSecret(key) {
			return errors.New("each user requires a 32-byte secret")
		}
	}
	if len(s.Listeners) > 0 && (s.Listen != "" || s.WSListen != "" || s.Certificate != "" || s.Key != "" || s.WSPath != "" || len(s.WSPaths) > 0) {
		return errors.New("choose listeners or legacy listener fields, not both")
	}
	listeners := s.listeners()
	if len(listeners) == 0 {
		return errors.New("enable at least one listener")
	}
	seen := map[string]bool{}
	for _, l := range listeners {
		if !validTarget(l.Listen) || seen[l.Listen] {
			return errors.New("invalid or duplicate listener address")
		}
		seen[l.Listen] = true
		switch l.Kind {
		case "tcp", "ws":
			if !loopback(l.Listen) {
				return errors.New("unencrypted listeners must be loopback")
			}
			if l.Certificate != "" || l.Key != "" {
				return errors.New("certificate fields belong to TLS/WSS listeners")
			}
		case "tls", "wss":
			if l.Certificate == "" || l.Key == "" {
				return errors.New("TLS/WSS listener requires certificate and key")
			}
		default:
			return errors.New("listener kind must be tcp, tls, ws or wss")
		}
		if l.Kind == "ws" || l.Kind == "wss" {
			for _, path := range append([]string{l.Path}, l.Paths...) {
				if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
					return errors.New("absolute WebSocket path required")
				}
			}
		}
	}
	for _, h := range s.ExitChain {
		if (h.Kind != "socks5" && h.Kind != "http") || !validTarget(h.Address) {
			return errors.New("invalid server exit bridge")
		}
	}
	return nil
}
func LoadServer(raw []byte) (ServerConfig, error) {
	var s ServerConfig
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if e := d.Decode(&s); e != nil {
		return s, e
	}
	if d.Decode(new(any)) != io.EOF {
		return s, errors.New("trailing server data")
	}
	return s, s.Validate()
}
func (l Library) ServerTemplate() ServerConfig {
	paths := []string{}
	seen := map[string]bool{"/puk-bond": true}
	for _, p := range l.Profiles {
		u, e := url.Parse(p.URL)
		if e == nil && (u.Scheme == "ws" || u.Scheme == "wss") {
			path := u.Path
			if path == "" {
				path = "/"
			}
			if !seen[path] {
				paths = append(paths, path)
				seen[path] = true
			}
		}
	}
	return ServerConfig{ID: l.ServerID, Users: map[string]string{l.User: l.Secret}, Listeners: []Listener{{Kind: "tcp", Listen: "127.0.0.1:18085"}, {Kind: "ws", Listen: "127.0.0.1:18086", Path: "/puk-bond", Paths: paths}}, MaxSessions: 128}
}

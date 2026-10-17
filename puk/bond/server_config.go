package bond

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

type ServerConfig struct {
	WSPaths     []string          `json:"ws_paths,omitempty"`
	ID          string            `json:"id"`
	Users       map[string]string `json:"users"`
	Listen      string            `json:"listen"`
	WSListen    string            `json:"ws_listen"`
	WSPath      string            `json:"ws_path"`
	Certificate string            `json:"certificate,omitempty"`
	Key         string            `json:"key,omitempty"`
	ExitChain   []Hop             `json:"exit_chain,omitempty"`
	MaxSessions int               `json:"max_sessions,omitempty"`
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
	if s.Listen == "" && s.WSListen == "" {
		return errors.New("enable a TCP/TLS or WebSocket listener")
	}
	for _, a := range []string{s.Listen, s.WSListen} {
		if a != "" {
			if !validTarget(a) {
				return errors.New("invalid listener address")
			}
			if s.Certificate == "" && !loopback(a) {
				return errors.New("public listeners require TLS; CF and s-ui can reach loopback adapters")
			}
		}
	}
	if s.Certificate != "" && s.Key == "" || s.Key != "" && s.Certificate == "" {
		return errors.New("both certificate and key required")
	}
	if s.WSListen != "" && (!strings.HasPrefix(s.WSPath, "/") || strings.ContainsAny(s.WSPath, "?#")) {
		return errors.New("absolute WebSocket path required")
	}
	for _, h := range s.ExitChain {
		if (h.Kind != "socks5" && h.Kind != "http") || !validTarget(h.Address) {
			return errors.New("invalid server exit bridge")
		}
	}
	for _, p := range s.WSPaths {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "?#") {
			return errors.New("invalid additional WS path")
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
	seen := map[string]bool{}
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
	return ServerConfig{ID: l.ServerID, Users: map[string]string{l.User: l.Secret}, Listen: "127.0.0.1:18085", WSListen: "127.0.0.1:18086", WSPath: "/puk-bond", WSPaths: paths, MaxSessions: 128}
}

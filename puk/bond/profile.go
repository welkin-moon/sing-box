package bond

import (
	"encoding/hex"
	"errors"
	"net"
	"net/url"
)

type Hop struct {
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Path is a saved configuration profile. Chain is ordered client -> entry -> exit.
// An existing sing-box/Xray profile can supply a loopback SOCKS bridge as a hop.
type Path struct {
	TrustedBridge     bool   `json:"-"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	Hub               string `json:"hub"`
	URL               string `json:"url"` // tls://host:port, wss://host/path, or tcp/ws over an explicit private path
	ServerName        string `json:"server_name,omitempty"`
	CertificateSHA256 string `json:"certificate_sha256,omitempty"`
	Chain             []Hop  `json:"chain,omitempty"`
}

func loopback(addr string) bool {
	h, _, e := net.SplitHostPort(addr)
	ip := net.ParseIP(h)
	return e == nil && ip != nil && ip.IsLoopback()
}
func (p Path) Validate() error {
	if p.ID == "" || p.Hub == "" {
		return errors.New("path ID and hub ID required")
	}
	u, e := url.Parse(p.URL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid path URL")
	}
	switch u.Scheme {
	case "tls", "tcp":
		if !validTarget(u.Host) || u.Path != "" {
			return errors.New("TCP/TLS URL needs host:port and no path")
		}
	case "ws", "wss":
		if u.Hostname() == "" {
			return errors.New("invalid WS host")
		}
	default:
		return errors.New("transport must be tcp, tls, ws or wss")
	}
	if u.Scheme == "tcp" || u.Scheme == "ws" {
		host := u.Host
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
		if !loopback(host) || len(p.Chain) == 0 && !p.TrustedBridge {
			return errors.New("unencrypted adapters require loopback destination through an explicit proxy chain")
		}
	}
	if p.CertificateSHA256 != "" {
		b, e := hex.DecodeString(p.CertificateSHA256)
		if e != nil || len(b) != 32 {
			return errors.New("certificate pin must be 64 hex characters")
		}
	}
	for _, h := range p.Chain {
		if h.Kind != "socks5" && h.Kind != "http" {
			return errors.New("chain bridges must be socks5 or http")
		}
		if !validTarget(h.Address) {
			return errors.New("invalid bridge address")
		}
		if len(h.Username) > 255 || len(h.Password) > 255 {
			return errors.New("SOCKS credentials too long")
		}
	}
	return nil
}

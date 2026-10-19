// Package split builds an authenticated two-path proxy without changing system routes.
// XHTTP associates the streams; its download dialer alone uses Hysteria2.
package split

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
)

type HY2 struct {
	Server            string `json:"server"`
	Port              int    `json:"port"`
	Password          string `json:"password"`
	ServerName        string `json:"server_name"`
	CertificateSHA256 string `json:"certificate_sha256"`
}

type Profile struct {
	Name        string `json:"name"`
	UploadHost  string `json:"upload_host"`
	UploadPort  int    `json:"upload_port"`
	Path        string `json:"path"`
	UUID        string `json:"uuid"`
	BackendPort int    `json:"backend_port"`
	Download    HY2    `json:"download"`
}

func validPort(p int) bool { return p > 0 && p <= 65535 }

func (p Profile) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if p.UploadHost == "" || strings.ContainsAny(p.UploadHost, "/\\:@?# \t\r\n") {
		return errors.New("upload_host must be a DNS hostname")
	}
	if !validPort(p.UploadPort) || !validPort(p.BackendPort) || !validPort(p.Download.Port) {
		return errors.New("ports must be in 1..65535")
	}
	u, e := url.Parse(p.Path)
	if e != nil || !strings.HasPrefix(p.Path, "/") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(p.Path, "\r\n") {
		return errors.New("path must be an absolute HTTP path without query or fragment")
	}
	if len(p.UUID) != 36 || p.UUID[8] != '-' || p.UUID[13] != '-' || p.UUID[18] != '-' || p.UUID[23] != '-' {
		return errors.New("uuid is invalid")
	}
	if b, e := hex.DecodeString(strings.ReplaceAll(p.UUID, "-", "")); e != nil || len(b) != 16 {
		return errors.New("uuid is invalid")
	}
	if p.Download.Server == "" || strings.ContainsAny(p.Download.Server, "/\\ \t\r\n") {
		return errors.New("download.server is invalid")
	}
	if p.Download.Password == "" || p.Download.ServerName == "" {
		return errors.New("HY2 password and server_name are required")
	}
	if b, e := hex.DecodeString(p.Download.CertificateSHA256); e != nil || len(b) != 32 {
		return errors.New("HY2 certificate_sha256 must be 64 hexadecimal characters")
	}
	return nil
}

type object = map[string]any

// Config returns a complete Xray config. Set both ports to zero for a GUI host
// that injects its own SOCKS bridge and owns VpnService/TUN socket protection.
func Config(p Profile, socksPort, httpPort int) (object, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	if socksPort != 0 && !validPort(socksPort) || httpPort != 0 && !validPort(httpPort) || socksPort != 0 && socksPort == httpPort {
		return nil, errors.New("invalid or overlapping local ports")
	}
	download := object{
		"address": "127.0.0.1", "port": p.BackendPort, "network": "xhttp", "security": "none",
		"sockopt":       object{"dialerProxy": "hy2-download"},
		"xhttpSettings": object{"host": p.UploadHost, "path": p.Path, "mode": "packet-up"},
	}
	xhttp := object{
		"tag": "split", "protocol": "vless",
		"settings": object{"vnext": []any{object{"address": p.UploadHost, "port": p.UploadPort, "users": []any{object{"id": p.UUID, "encryption": "none"}}}}},
		"streamSettings": object{
			"network": "xhttp", "security": "tls",
			"tlsSettings":   object{"serverName": p.UploadHost, "alpn": []string{"h2"}, "fingerprint": "chrome"},
			"xhttpSettings": object{"host": p.UploadHost, "path": p.Path, "mode": "packet-up", "extra": object{"scMaxEachPostBytes": 1000000, "scMinPostsIntervalMs": 0, "downloadSettings": download}},
		},
	}
	hy2 := object{
		"tag": "hy2-download", "protocol": "hysteria",
		"settings": object{"version": 2, "address": p.Download.Server, "port": p.Download.Port},
		"streamSettings": object{"network": "hysteria", "security": "tls",
			"tlsSettings":      object{"serverName": p.Download.ServerName, "alpn": []string{"h3"}, "pinnedPeerCertSha256": p.Download.CertificateSHA256},
			"hysteriaSettings": object{"version": 2, "auth": p.Download.Password},
		},
	}
	cfg := object{"remarks": p.Name, "log": object{"loglevel": "warning"}, "outbounds": []any{xhttp, hy2},
		// XHTTP handles TCP payloads. Native UDP takes HY2 in both directions.
		"routing": object{"rules": []any{object{"type": "field", "network": "udp", "outboundTag": "hy2-download"}}},
	}
	var inbounds []any
	if socksPort != 0 {
		inbounds = append(inbounds, object{"tag": "puk-socks", "listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": object{"auth": "noauth", "udp": true}})
	}
	if httpPort != 0 {
		inbounds = append(inbounds, object{"tag": "puk-http", "listen": "127.0.0.1", "port": httpPort, "protocol": "http", "settings": object{}})
	}
	if len(inbounds) != 0 {
		cfg["inbounds"] = inbounds
	}
	return cfg, nil
}

// ThroneLink preserves the whole dialerProxy graph. Exporting just the first
// VLESS outbound would silently discard the HY2 download leg.
func ThroneLink(p Profile) (string, error) {
	c, e := Config(p, 0, 0)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	wrapper := object{"name": p.Name, "type": "custom", "subtype": "xrayfullconfig", "config": string(b)}
	b, e = json.Marshal(wrapper)
	if e != nil {
		return "", e
	}
	return "throne://add/" + base64.RawURLEncoding.EncodeToString(b), nil
}

func Load(raw []byte) (Profile, error) {
	var p Profile
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if e := d.Decode(&p); e != nil {
		return p, fmt.Errorf("decode profile: %w", e)
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return p, errors.New("unexpected data after profile")
	}
	return p, p.Validate()
}

// LoopbackAddress is shared by launchers; no interface-wide listener is used.
func LoopbackAddress(port int) string { return net.JoinHostPort("127.0.0.1", fmt.Sprint(port)) }

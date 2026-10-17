package core

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/welkin-moon/sing-box/puk/model"
)

type Attachment struct {
	Tunnel    model.Tunnel `json:"tunnel"`
	SourceTag string       `json:"source_tag"`
	Tag       string       `json:"tag"`
}

// Attach duplicates a TCP outbound; preserves its remote TLS identity and keys.
// The supervisor forwards its loopback server to the remote Access hostname.
func Attach(raw, attachments []byte) ([]byte, []model.Tunnel, error) {
	var c map[string]any
	var a []Attachment
	if e := json.Unmarshal(raw, &c); e != nil {
		return nil, nil, e
	}
	if e := json.Unmarshal(attachments, &a); e != nil {
		return nil, nil, e
	}
	outs, ok := c["outbounds"].([]any)
	if !ok {
		return nil, nil, fmt.Errorf("missing outbounds")
	}
	index := map[string]map[string]any{}
	for _, v := range outs {
		m, _ := v.(map[string]any)
		tag, _ := m["tag"].(string)
		index[tag] = m
	}
	tunnels := []model.Tunnel{}
	for _, x := range a {
		source := index[x.SourceTag]
		if source == nil || x.Tag == "" || index[x.Tag] != nil {
			return nil, nil, fmt.Errorf("invalid or duplicate attachment tag")
		}
		typ, _ := source["type"].(string)
		if typ != "vless" && typ != "trojan" && typ != "vmess" {
			return nil, nil, fmt.Errorf("attachment needs a TCP protocol")
		}
		if network, ok := source["network"].(string); ok && network == "udp" {
			return nil, nil, fmt.Errorf("UDP cannot use Access TCP")
		}
		h, p, e := net.SplitHostPort(x.Tunnel.Listen)
		if e != nil {
			return nil, nil, e
		}
		ip := net.ParseIP(h)
		if ip == nil || !ip.IsLoopback() {
			return nil, nil, fmt.Errorf("attachment listen must be loopback")
		}
		port, e := strconv.Atoi(p)
		if e != nil || port < 1024 || port > 65535 {
			return nil, nil, fmt.Errorf("bad tunnel port")
		}
		b, _ := json.Marshal(source)
		var node map[string]any
		json.Unmarshal(b, &node)
		if tls, ok := node["tls"].(map[string]any); ok {
			if s, _ := tls["server_name"].(string); s == "" {
				server, _ := source["server"].(string)
				if net.ParseIP(server) != nil {
					return nil, nil, fmt.Errorf("TLS server_name required for IP source")
				}
				tls["server_name"] = server
			}
		}
		node["tag"] = x.Tag
		node["server"] = h
		node["server_port"] = port
		outs = append(outs, node)
		index[x.Tag] = node
		tunnels = append(tunnels, x.Tunnel)
	}
	c["outbounds"] = outs
	b, e := json.Marshal(c)
	return b, tunnels, e
}

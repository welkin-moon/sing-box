package bond

// AndroidConfig supplies a VPN ingress and a TCP-capable DNS path. Transport
// profiles still use the host's protected dialers, independently of the TUN.
func (l Library) AndroidConfig() (map[string]any, error) {
	c, err := l.NativeConfig()
	if err != nil {
		return nil, err
	}
	c["inbounds"] = []any{map[string]any{
		"type": "tun", "tag": "puk-vpn", "address": []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
		"mtu": 1500, "auto_route": true, "strict_route": true, "stack": "system",
	}}
	route := c["route"].(map[string]any)
	route["rules"] = []any{
		map[string]any{"action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
	}
	dns, _ := c["dns"].(map[string]any)
	if len(dns) == 0 {
		c["dns"] = map[string]any{
			"servers": []any{
				map[string]any{"type": "local", "tag": "puk-bootstrap"},
				map[string]any{"type": "https", "tag": "puk-remote-dns", "server": "1.1.1.1", "server_port": 443,
					"path": "/dns-query", "tls": map[string]any{"enabled": true, "server_name": "cloudflare-dns.com"}, "detour": "PUK-Bond"},
			},
			"final": "puk-remote-dns",
		}
		route["default_domain_resolver"] = "puk-bootstrap"
	}
	return c, nil
}

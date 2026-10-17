// Package core preserves subscription nodes and adds isolated probe inbounds.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/welkin-moon/sing-box/puk/model"
)

func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Generate does not remove or rewrite any existing outbound. Only concrete
// proxy outbounds are eligible; Google Direct variants have a separate policy.
func Generate(raw []byte, basePort int) ([]byte, model.Config, error) {
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, model.Config{}, err
	}
	if basePort < 1024 || basePort > 65000 || basePort == 19100 || basePort == 19101 {
		return nil, model.Config{}, fmt.Errorf("invalid or reserved base port")
	}
	outs, ok := c["outbounds"].([]any)
	if !ok {
		return nil, model.Config{}, fmt.Errorf("subscription needs outbounds")
	}
	cfg := model.Config{Listen: "127.0.0.1:19100", Token: Secret(), Controller: "http://127.0.0.1:19101", ControllerSecret: Secret(), IntervalSeconds: 120}
	probes := []model.Probe{
		{URL: "https://www.gstatic.com/generate_204", Statuses: []int{204}, TimeoutSeconds: 15},
		{URL: "https://chatgpt.com/cdn-cgi/trace", Statuses: []int{200}, MinBytes: 32, TimeoutSeconds: 15},
		{URL: "https://www.microsoft.com/", Statuses: []int{200}, MinBytes: 4096, TimeoutSeconds: 20},
	}
	policies := []model.Policy{
		{Selector: "PUK-Auto", Probes: probes, Failures: 2, Recoveries: 2, HoldSeconds: 120},
		{Selector: "PUK-Google-Direct", Probes: probes, Failures: 2, Recoveries: 2, HoldSeconds: 120},
	}
	ins, _ := c["inbounds"].([]any)
	usedPorts := map[int]bool{basePort: true, 19100: true, 19101: true}
	usedInboundTags := map[string]bool{}
	for _, v := range ins {
		m, _ := v.(map[string]any)
		t, _ := m["tag"].(string)
		usedInboundTags[t] = true
		if p, ok := m["listen_port"].(float64); ok {
			if int(p) == basePort || int(p) == 19100 || int(p) == 19101 {
				return nil, cfg, fmt.Errorf("existing inbound uses reserved port")
			}
			usedPorts[int(p)] = true
		}
	}
	route, _ := c["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
	}
	rules, _ := route["rules"].([]any)
	var probeRules []any
	seen := map[string]bool{}
	for _, v := range outs {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := m["tag"].(string)
		if tag == "" || seen[tag] {
			return nil, cfg, fmt.Errorf("missing or duplicate outbound tag")
		}
		seen[tag] = true
	}
	for _, name := range []string{"PUK-Auto", "PUK-Google-Direct"} {
		if seen[name] {
			return nil, cfg, fmt.Errorf("generated tag collision: %s", name)
		}
	}
	count := 0
	nextPort := basePort + 1
	for _, v := range outs {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		tag, _ := m["tag"].(string)
		switch typ {
		case "vless", "hysteria2", "trojan", "vmess", "shadowsocks", "tuic", "socks", "http":
		default:
			continue
		}
		for usedPorts[nextPort] {
			nextPort++
		}
		port := nextPort
		nextPort++
		if port > 65535 {
			return nil, cfg, fmt.Errorf("too many nodes")
		}
		usedPorts[port] = true
		inbound := fmt.Sprintf("PUK-Probe-%d", count)
		count++
		if usedInboundTags[inbound] {
			return nil, cfg, fmt.Errorf("probe inbound tag collision")
		}
		ins = append(ins, map[string]any{"type": "mixed", "tag": inbound, "listen": "127.0.0.1", "listen_port": port})
		probeRules = append(probeRules, map[string]any{"inbound": []string{inbound}, "action": "route", "outbound": tag})
		p := 0
		low := strings.ToLower(tag)
		if strings.Contains(low, "google-direct") || strings.Contains(tag, "不走WARP") {
			p = 1
		}
		priority := 10
		if typ == "hysteria2" {
			priority = 20
		}
		if strings.Contains(low, "cloudflared") || strings.Contains(low, "cf") {
			priority = 0
		}
		policies[p].Candidates = append(policies[p].Candidates, model.Candidate{Tag: tag, ProxyURL: fmt.Sprintf("http://127.0.0.1:%d", port), Priority: priority})
	}
	if count == 0 {
		return nil, cfg, fmt.Errorf("no supported proxy nodes")
	}
	for _, p := range policies {
		if len(p.Candidates) == 0 {
			continue
		}
		tags := []string{}
		defaultTag := p.Candidates[0].Tag
		bestPriority := p.Candidates[0].Priority
		for _, n := range p.Candidates {
			tags = append(tags, n.Tag)
			if n.Priority > bestPriority {
				defaultTag = n.Tag
				bestPriority = n.Priority
			}
		}
		outs = append(outs, map[string]any{"type": "selector", "tag": p.Selector, "outbounds": tags, "default": defaultTag, "interrupt_exist_connections": false})
		cfg.Policies = append(cfg.Policies, p)
	}
	if len(policies[0].Candidates) == 0 {
		return nil, cfg, fmt.Errorf("no default candidates; select Direct explicitly")
	}
	for _, v := range ins {
		m, _ := v.(map[string]any)
		if m["tag"] == "PUK-Main" {
			return nil, cfg, fmt.Errorf("main inbound collision")
		}
	}
	ins = append(ins, map[string]any{"type": "mixed", "tag": "PUK-Main", "listen": "127.0.0.1", "listen_port": basePort})
	probeRules = append(probeRules, map[string]any{"inbound": []string{"PUK-Main"}, "action": "route", "outbound": "PUK-Auto"})
	route["rules"] = append(probeRules, rules...)
	c["route"] = route
	c["inbounds"] = ins
	c["outbounds"] = outs
	experimental, _ := c["experimental"].(map[string]any)
	if experimental == nil {
		experimental = map[string]any{}
	}
	experimental["clash_api"] = map[string]any{"external_controller": "127.0.0.1:19101", "secret": cfg.ControllerSecret, "access_control_allow_origin": []string{}}
	c["experimental"] = experimental
	result, err := json.MarshalIndent(c, "", "  ")
	return result, cfg, err
}

// WriteNew refuses overwrite. Credentials belong in owner-only runtime files.
func WriteNew(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

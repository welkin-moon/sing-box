package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPreserveAndSeparate(t *testing.T) {
	raw := []byte(`{"outbounds":[{"type":"vless","tag":"UK-Reality-direct","server":"example.org","uuid":"old"},{"type":"hysteria2","tag":"UK-Google-Direct-HY2","password":"kept"},{"type":"direct","tag":"direct"}],"route":{"rules":[{"domain":["example.org"],"outbound":"direct"}]}}`)
	out, c, e := Generate(raw, 19080)
	if e != nil {
		t.Fatal(e)
	}
	if len(c.Policies) != 2 || c.Policies[0].Candidates[0].Tag != "UK-Reality-direct" || c.Policies[1].Candidates[0].Tag != "UK-Google-Direct-HY2" {
		t.Fatalf("bad separation: %+v", c.Policies)
	}
	var decoded map[string]any
	json.Unmarshal(out, &decoded)
	nodes := decoded["outbounds"].([]any)
	if nodes[0].(map[string]any)["uuid"] != "old" || nodes[1].(map[string]any)["password"] != "kept" {
		t.Fatal("credentials changed")
	}
	rules := decoded["route"].(map[string]any)["rules"].([]any)
	if len(rules) != 4 {
		t.Fatal("old rules not preserved")
	}
	if c.Token == c.ControllerSecret || len(c.Token) != 64 {
		t.Fatal("independent tokens required")
	}
}
func TestAttachmentPreservesReality(t *testing.T) {
	raw := []byte(`{"outbounds":[{"type":"vless","tag":"UK","server":"vps.example","server_port":443,"uuid":"old","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"sni.example","reality":{"enabled":true,"public_key":"public","short_id":"a1"}}}]}`)
	a := []byte(`[{"source_tag":"UK","tag":"UK-Access","tunnel":{"name":"UK","hostname":"tcp.example","listen":"127.0.0.1:19443","binary":"/usr/bin/cloudflared"}}]`)
	b, ts, e := Attach(raw, a)
	if e != nil {
		t.Fatal(e)
	}
	if len(ts) != 1 {
		t.Fatal("missing tunnel")
	}
	var c map[string]any
	json.Unmarshal(b, &c)
	nodes := c["outbounds"].([]any)
	n := nodes[1].(map[string]any)
	if n["server"] != "127.0.0.1" || n["flow"] != "xtls-rprx-vision" || n["tls"].(map[string]any)["server_name"] != "sni.example" {
		t.Fatal("identity mutated")
	}
	if nodes[0].(map[string]any)["server"] != "vps.example" {
		t.Fatal("original changed")
	}
}
func TestRejectOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if e := WriteNew(p, []byte("old")); e != nil {
		t.Fatal(e)
	}
	if e := WriteNew(p, []byte("new")); e == nil {
		t.Fatal("overwrote existing")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "old" {
		t.Fatal("backup lost")
	}
}
func TestGeneratedPortsUnique(t *testing.T) {
	nodes := []any{}
	for i := 0; i < 40; i++ {
		nodes = append(nodes, map[string]any{"type": "vless", "tag": fmt.Sprintf("node-%d", i)})
	}
	raw, _ := json.Marshal(map[string]any{"outbounds": nodes})
	out, _, e := Generate(raw, 19080)
	if e != nil {
		t.Fatal(e)
	}
	var c map[string]any
	json.Unmarshal(out, &c)
	seen := map[int]bool{19100: true, 19101: true}
	for _, v := range c["inbounds"].([]any) {
		m := v.(map[string]any)
		p := int(m["listen_port"].(float64))
		if seen[p] {
			t.Fatalf("port collision %d", p)
		}
		seen[p] = true
	}
}

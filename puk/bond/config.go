package bond

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

type Selection struct {
	Upload   string `json:"upload"`
	Download string `json:"download"`
}
type Profile struct {
	Path
	Outbound string `json:"outbound,omitempty"`
}
type Library struct {
	Version   int       `json:"version"`
	Name      string    `json:"name"`
	ServerID  string    `json:"server_id"`
	User      string    `json:"user"`
	Secret    string    `json:"secret"`
	Profiles  []Profile `json:"profiles"`
	Selection Selection `json:"selection"`
	// Full sing-box config. Preserve imported DNS/endpoints/detours; replace only local inbounds/routes on export.
	Core map[string]any `json:"core,omitempty"`
}

func (l Library) profile(id string) (Profile, bool) {
	for _, p := range l.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}
func (l Library) Validate() error {
	if l.Version != 1 || strings.TrimSpace(l.Name) == "" || l.ServerID == "" || l.User == "" {
		return errors.New("version 1, name, server_id and user required")
	}
	if !ValidSecret(l.Secret) {
		return errors.New("invalid secret: generate 32 random bytes")
	}
	seen := map[string]bool{}
	tags := map[string]bool{}
	if l.Core != nil {
		raw, ok := l.Core["outbounds"].([]any)
		if !ok {
			return errors.New("imported sing-box config needs outbounds")
		}
		for _, v := range raw {
			o, ok := v.(map[string]any)
			if !ok {
				return errors.New("invalid outbound")
			}
			tag, _ := o["tag"].(string)
			if tag == "" || tags[tag] || tag == "PUK-Bond" {
				return errors.New("missing, duplicate or reserved outbound tag")
			}
			tags[tag] = true
		}
	}
	for _, p := range l.Profiles {
		if p.ID == "" || seen[p.ID] {
			return errors.New("missing or duplicate profile ID")
		}
		seen[p.ID] = true
		if p.Hub != l.ServerID {
			return fmt.Errorf("profile %s belongs to a different hub", p.ID)
		}
		if p.Outbound != "" && !tags[p.Outbound] {
			return fmt.Errorf("profile %s references unknown outbound %s", p.ID, p.Outbound)
		}
		p.TrustedBridge = p.Outbound != ""
		if e := p.Validate(); e != nil {
			return fmt.Errorf("profile %s: %w", p.ID, e)
		}
	}
	if _, ok := l.profile(l.Selection.Upload); !ok {
		return errors.New("select an upload profile")
	}
	if _, ok := l.profile(l.Selection.Download); !ok {
		return errors.New("select a download profile")
	}
	return validateCoreGraph(l.Core)
}
func Load(raw []byte) (Library, error) {
	var l Library
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&l); e != nil {
		return l, e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return l, errors.New("trailing config data")
	}
	return l, l.Validate()
}
func (l Library) NativeConfig() (map[string]any, error) {
	if e := l.Validate(); e != nil {
		return nil, e
	}
	c := map[string]any{}
	if l.Core != nil {
		b, _ := json.Marshal(l.Core)
		json.Unmarshal(b, &c)
	}
	outs, _ := c["outbounds"].([]any)
	leg := func(id string) map[string]any {
		p, _ := l.profile(id)
		v := map[string]any{"url": p.URL}
		if p.Outbound != "" {
			v["outbound"] = p.Outbound
		}
		if p.ServerName != "" {
			v["server_name"] = p.ServerName
		}
		if p.CertificateSHA256 != "" {
			v["certificate_sha256"] = p.CertificateSHA256
		}
		return v
	}
	// Native outbounds choose imported core profiles directly. Generic bridge chains remain CLI-only.
	for _, id := range []string{l.Selection.Upload, l.Selection.Download} {
		p, _ := l.profile(id)
		if len(p.Chain) > 0 {
			return nil, errors.New("native export: represent the bridge chain as sing-box outbounds with detour, or use CLI bridge mode")
		}
	}
	o := map[string]any{"type": "puk-bond", "tag": "PUK-Bond", "server_id": l.ServerID, "user": l.User, "secret": l.Secret, "upload": leg(l.Selection.Upload), "download": leg(l.Selection.Download)}
	c["outbounds"] = append(outs, o)
	c["inbounds"] = []any{map[string]any{"type": "mixed", "tag": "puk-local", "listen": "127.0.0.1", "listen_port": 19080}}
	route, _ := c["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
	}
	delete(route, "rules")
	route["final"] = "PUK-Bond"
	c["route"] = route
	delete(c, "experimental")
	delete(c, "services")
	return c, nil
}

// Client uses injectable path dialers in hosts. CLI bridge mode uses standard proxy hops.
func (l Library) Client(base DialFunc) (Client, error) {
	if e := l.Validate(); e != nil {
		return Client{}, e
	}
	up, _ := l.profile(l.Selection.Upload)
	down, _ := l.profile(l.Selection.Download)
	if up.Outbound != "" || down.Outbound != "" {
		return Client{}, errors.New("core outbound profiles require the patched sing-box core; export config and use --core")
	}
	return Client{Server: l.ServerID, User: l.User, Secret: l.Secret, Upload: up.Path.Dial(base), Download: down.Path.Dial(base)}, nil
}
func (c Client) ServeSOCKS(ctx context.Context, ln net.Listener) error {
	return ServeSOCKS(ctx, ln, func(ctx context.Context, _, address string) (net.Conn, error) { return c.Open(ctx, address) })
}

func validateCoreGraph(c map[string]any) error {
	nodes := map[string]map[string]any{}
	for _, key := range []string{"outbounds", "endpoints"} {
		list, _ := c[key].([]any)
		for _, v := range list {
			m, ok := v.(map[string]any)
			if !ok {
				return errors.New("invalid core node")
			}
			tag, _ := m["tag"].(string)
			if tag == "" || nodes[tag] != nil {
				return errors.New("duplicate/missing core node tag")
			}
			nodes[tag] = m
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(tag string) error {
		if state[tag] == 1 {
			return fmt.Errorf("proxy chain cycle at %s", tag)
		}
		if state[tag] == 2 {
			return nil
		}
		node, ok := nodes[tag]
		if !ok {
			return fmt.Errorf("unknown chain node %s", tag)
		}
		state[tag] = 1
		var refs []string
		if d, ok := node["detour"].(string); ok && d != "" {
			refs = append(refs, d)
		}
		if typ := node["type"]; typ == "selector" || typ == "urltest" {
			if a, ok := node["outbounds"].([]any); ok {
				for _, v := range a {
					if r, ok := v.(string); ok {
						refs = append(refs, r)
					}
				}
			}
		}
		for _, r := range refs {
			if e := visit(r); e != nil {
				return e
			}
		}
		state[tag] = 2
		return nil
	}
	for tag := range nodes {
		if e := visit(tag); e != nil {
			return e
		}
	}
	return nil
}

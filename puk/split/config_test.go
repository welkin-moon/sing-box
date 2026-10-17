package split

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func example() Profile {
	return Profile{Name: "TW-Split-direct", UploadHost: "upload.example.org", UploadPort: 443, Path: "/split/", UUID: "01234567-89ab-cdef-0123-456789abcdef", BackendPort: 18084, Download: HY2{Server: "192.0.2.1", Port: 11389, Password: "test-only", ServerName: "hy2.example.org", CertificateSHA256: strings.Repeat("ab", 32)}}
}

func TestSplitGraphAndGUIRoundTrip(t *testing.T) {
	p := example()
	cfg, e := Config(p, 19080, 19081)
	if e != nil {
		t.Fatal(e)
	}
	out := cfg["outbounds"].([]any)
	if len(out) != 2 {
		t.Fatal("unexpected fallbacks could bypass split")
	}
	upload := out[0].(object)["streamSettings"].(object)
	if upload["security"] != "tls" || upload["sockopt"] != nil {
		t.Fatal("upload leg must go directly to CF over TLS")
	}
	down := upload["xhttpSettings"].(object)["extra"].(object)["downloadSettings"].(object)
	if down["sockopt"].(object)["dialerProxy"] != "hy2-download" || down["address"] != "127.0.0.1" {
		t.Fatal("download bypassed HY2")
	}
	if out[1].(object)["tag"] != "hy2-download" {
		t.Fatal("download dialer tag is dangling")
	}
	tls := out[1].(object)["streamSettings"].(object)["tlsSettings"].(object)
	if tls["pinnedPeerCertSha256"] != p.Download.CertificateSHA256 || tls["allowInsecure"] != nil {
		t.Fatal("HY2 peer verification missing")
	}
	for _, i := range cfg["inbounds"].([]any) {
		if i.(object)["listen"] != "127.0.0.1" {
			t.Fatal("public proxy listener")
		}
	}
	link, e := ThroneLink(p)
	if e != nil {
		t.Fatal(e)
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "throne://add/"))
	if e != nil {
		t.Fatal(e)
	}
	var wrapper struct{ Type, Subtype, Name, Config string }
	if e = json.Unmarshal(b, &wrapper); e != nil {
		t.Fatal(e)
	}
	if wrapper.Type != "custom" || wrapper.Subtype != "xrayfullconfig" {
		t.Fatal("GUI would import incomplete outbound")
	}
	var imported object
	if e = json.Unmarshal([]byte(wrapper.Config), &imported); e != nil {
		t.Fatal(e)
	}
	if _, ok := imported["inbounds"]; ok {
		t.Fatal("GUI must own its bridge ports")
	}
	if len(imported["outbounds"].([]any)) != 2 {
		t.Fatal("GUI round-trip lost HY2")
	}
	if !strings.Contains(wrapper.Config, "dialerProxy") {
		t.Fatal("GUI round-trip lost download routing")
	}
}

func TestRejectUnsafeOrAmbiguousProfiles(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Profile)
	}{
		{"missing pin", func(p *Profile) { p.Download.CertificateSHA256 = "" }},
		{"bad pin", func(p *Profile) { p.Download.CertificateSHA256 = strings.Repeat("zz", 32) }},
		{"bad upload host", func(p *Profile) { p.UploadHost = "https://upload.example.org/" }},
		{"bad path", func(p *Profile) { p.Path = "//other.example/path" }},
		{"path query", func(p *Profile) { p.Path = "/split/?secret=x" }},
		{"bad UUID", func(p *Profile) { p.UUID = "x1234567-89ab-cdef-0123-456789abcdef" }},
		{"empty HY2 password", func(p *Profile) { p.Download.Password = "" }},
		{"bad server port", func(p *Profile) { p.Download.Port = 65536 }},
		{"bad backend port", func(p *Profile) { p.BackendPort = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := example()
			tc.mutate(&p)
			if _, e := Config(p, 19080, 19081); e == nil {
				t.Fatal("accepted invalid profile")
			}
		})
	}
	p := example()
	if _, e := Config(p, 19080, 19080); e == nil {
		t.Fatal("overlapping ports accepted")
	}
	if _, e := Config(p, -1, 0); e == nil {
		t.Fatal("negative listener port accepted")
	}
}

func TestLoadRejectsTyposAndTrailingContent(t *testing.T) {
	b, _ := json.Marshal(example())
	if _, e := Load(b); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{string(b) + " {}", string(b) + " broken", strings.Replace(string(b), "upload_host", "uplod_host", 1)} {
		if _, e := Load([]byte(raw)); e == nil {
			t.Fatal("accepted ambiguous input")
		}
	}
}

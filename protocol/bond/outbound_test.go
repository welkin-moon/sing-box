package bond_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	AO "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	PB "github.com/sagernet/sing-box/protocol/bond"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	B "github.com/welkin-moon/sing-box/puk/bond"
)

func TestGeneratedAndroidConfigParses(t *testing.T) {
	l, err := B.Load(B.ExampleLibrary)
	if err != nil {
		t.Fatal(err)
	}
	c, err := l.AndroidConfig()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var opts option.Options
	if err = json.UnmarshalContext(include.Context(t.Context()), raw, &opts); err != nil {
		t.Fatal(err)
	}
	if len(opts.Inbounds) != 1 || opts.Inbounds[0].Type != "tun" {
		t.Fatal("Android VPN ingress missing")
	}
}

func TestNativeOutboundWithSelectorAndChain(t *testing.T) {
	ctx := t.Context()
	secret := strings.Repeat("ab", 32)
	h := &B.Hub{ID: "jp-hub", Users: map[string]string{"u": secret}}
	defer h.Close()
	ws := httptest.NewTLSServer(h.WebSocketHandler("/bond"))
	defer ws.Close()
	sum := sha256.Sum256(ws.Certificate().Raw)
	pin := hex.EncodeToString(sum[:])
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	tl := tls.NewListener(ln, &tls.Config{Certificates: ws.TLS.Certificates, MinVersion: tls.VersionTLS12})
	defer tl.Close()
	go func() {
		for {
			c, e := tl.Accept()
			if e != nil {
				return
			}
			go h.ServeConn(c)
		}
	}()
	var bridges []net.Listener
	for range 2 {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		bridges = append(bridges, l)
		defer l.Close()
		go B.ServeSOCKS(ctx, l, (&net.Dialer{}).DialContext)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "native split works") }))
	defer target.Close()
	raw := fmt.Sprintf(`{"log":{"disabled":true},"outbounds":[{"type":"socks","tag":"tw","server":"127.0.0.1","server_port":%d},{"type":"socks","tag":"jp-via-tw","server":"127.0.0.1","server_port":%d,"detour":"tw"},{"type":"selector","tag":"upload-profile","outbounds":["jp-via-tw"]},{"type":"puk-bond","tag":"bond","server_id":"jp-hub","user":"u","secret":%q,"upload":{"url":%q,"certificate_sha256":%q,"outbound":"upload-profile"},"download":{"url":%q,"certificate_sha256":%q}}],"route":{"final":"bond"}}`, bridges[0].Addr().(*net.TCPAddr).Port, bridges[1].Addr().(*net.TCPAddr).Port, secret, "wss"+strings.TrimPrefix(ws.URL, "https")+"/bond", pin, "tls://"+ln.Addr().String(), pin)
	coreCtx := include.Context(ctx)
	var opts option.Options
	if e = json.UnmarshalContext(coreCtx, []byte(raw), &opts); e != nil {
		t.Fatal(e)
	}
	instance, e := box.New(box.Options{Context: coreCtx, Options: opts})
	if e != nil {
		if runtime.GOOS == "linux" && strings.Contains(e.Error(), "create netlink socket: operation not permitted") {
			t.Skip("sandbox denies netlink; exercised on normal CI runners")
		}
		t.Fatal(e)
	}
	defer instance.Close()
	if e = instance.Start(); e != nil {
		if runtime.GOOS == "linux" && strings.Contains(e.Error(), "create netlink socket: operation not permitted") {
			t.Skip("sandbox denies netlink; exercised on normal CI runners")
		}
		t.Fatal(e)
	}
	out, ok := instance.Outbound().Outbound("bond")
	if !ok {
		t.Fatal("native type not registered")
	}
	c, e := out.DialContext(ctx, "tcp", M.ParseSocksaddr(strings.TrimPrefix(target.URL, "http://")))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	resp, e := http.ReadResponse(bufio.NewReader(c), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(resp.Body)
	if e != nil || string(data) != "native split works" {
		t.Fatal(string(data), e)
	}
}

type profileManager struct {
	adapter.OutboundManager
	items map[string]adapter.Outbound
}

func (m *profileManager) Outbound(tag string) (adapter.Outbound, bool) {
	o, ok := m.items[tag]
	return o, ok
}

type existingProfile struct {
	AO.Adapter
	h *B.Hub
}

func (p *existingProfile) DialContext(ctx context.Context, n string, a M.Socksaddr) (net.Conn, error) {
	c, s := net.Pipe()
	go p.h.ServeConn(s)
	return c, nil
}

func (p *existingProfile) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func TestNativeProfileSelectionWithoutPlatform(t *testing.T) {
	secret := strings.Repeat("bc", 32)
	h := &B.Hub{ID: "jp", Users: map[string]string{"u": secret}}
	defer h.Close()
	manager := &profileManager{items: map[string]adapter.Outbound{}}
	for _, tag := range []string{"tw-jp", "jp-direct"} {
		manager.items[tag] = &existingProfile{Adapter: AO.NewAdapter("test-profile", tag, []string{"tcp"}, nil), h: h}
	}
	ctx := service.ContextWith[adapter.OutboundManager](t.Context(), manager)
	o, e := PB.NewOutbound(ctx, nil, nil, "bond", option.BondOutboundOptions{ServerID: "jp", User: "u", Secret: secret, Upload: option.BondPathOptions{URL: "tcp://127.0.0.1:18085", Outbound: "tw-jp"}, Download: option.BondPathOptions{URL: "tcp://127.0.0.1:18085", Outbound: "jp-direct"}})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(o.Dependencies(), ",") != "tw-jp,jp-direct" {
		t.Fatal("lost selectable path dependencies")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "profiles work") }))
	defer target.Close()
	c, e := o.DialContext(ctx, "tcp", M.ParseSocksaddr(strings.TrimPrefix(target.URL, "http://")))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	resp, e := http.ReadResponse(bufio.NewReader(c), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(resp.Body)
	if e != nil || string(data) != "profiles work" {
		t.Fatal(string(data), e)
	}
}

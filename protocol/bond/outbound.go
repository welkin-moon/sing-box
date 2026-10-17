package bond

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	B "github.com/welkin-moon/sing-box/puk/bond"
)

const Type = "puk-bond"

func RegisterOutbound(r *outbound.Registry) {
	outbound.Register[option.BondOutboundOptions](r, Type, NewOutbound)
}

type Outbound struct {
	outbound.Adapter
	client B.Client
}

var _ adapter.Outbound = (*Outbound)(nil)

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, o option.BondOutboundOptions) (adapter.Outbound, error) {
	if o.ServerID == "" || o.User == "" {
		return nil, errors.New("puk-bond requires server_id and user")
	}
	var deps []string
	makeLeg := func(v option.BondPathOptions) (func(context.Context) (net.Conn, error), error) {
		if v.Outbound == tag {
			return nil, errors.New("bond leg cannot detour into itself")
		}
		if v.Outbound != "" {
			deps = append(deps, v.Outbound)
		}
		u, parseErr := url.Parse(v.URL)
		if parseErr != nil {
			return nil, parseErr
		}
		d, e := dialer.NewWithOptions(dialer.Options{Context: ctx, Options: option.DialerOptions{Detour: v.Outbound}, RemoteIsDomain: net.ParseIP(u.Hostname()) == nil, DisableEmptyDirectCheck: true})
		if e != nil {
			return nil, e
		}
		p := B.Path{ID: "leg", Hub: o.ServerID, URL: v.URL, ServerName: v.ServerName, CertificateSHA256: v.CertificateSHA256, TrustedBridge: v.Outbound != ""}
		if e = p.Validate(); e != nil {
			return nil, e
		}
		return p.Dial(func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, M.ParseSocksaddr(address))
		}), nil
	}
	up, e := makeLeg(o.Upload)
	if e != nil {
		return nil, e
	}
	down, e := makeLeg(o.Download)
	if e != nil {
		return nil, e
	}
	// Validate key length without exposing it in logs/errors.
	if !B.ValidSecret(o.Secret) {
		return nil, errors.New("puk-bond secret must be 64 hexadecimal characters")
	}
	return &Outbound{Adapter: outbound.NewAdapter(Type, tag, []string{N.NetworkTCP}, deps), client: B.Client{Server: o.ServerID, User: o.User, Secret: o.Secret, Upload: up, Download: down}}, nil
}
func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if N.NetworkName(network) != N.NetworkTCP {
		return nil, errors.New("puk-bond native UDP is not implemented")
	}
	return o.client.Open(ctx, destination.String())
}
func (o *Outbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("puk-bond native UDP is not implemented")
}

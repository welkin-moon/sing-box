package option

// Bond legs select existing outbounds (including selectors and detour chains).
// URLs address the shared final hub, not the ultimate website.
type BondPathOptions struct {
	URL               string `json:"url"`
	Outbound          string `json:"outbound,omitempty" reference:"outbound"`
	ServerName        string `json:"server_name,omitempty"`
	CertificateSHA256 string `json:"certificate_sha256,omitempty"`
}
type BondOutboundOptions struct {
	ServerID string          `json:"server_id"`
	User     string          `json:"user"`
	Secret   string          `json:"secret"`
	Upload   BondPathOptions `json:"upload"`
	Download BondPathOptions `json:"download"`
}

package cloudflare

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/shared"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

// Tunnel is the operator's view of a Cloudflare Tunnel.
type Tunnel struct {
	ID           string
	Name         string
	Status       string
	RemoteConfig bool
	Connections  int
}

func fromTunnel(t shared.CloudflareTunnel) Tunnel {
	return Tunnel{
		ID:           t.ID,
		Name:         t.Name,
		Status:       string(t.Status),
		RemoteConfig: t.RemoteConfig,
		Connections:  len(t.Connections),
	}
}

// GetTunnel fetches a tunnel by ID.
func (c *Client) GetTunnel(ctx context.Context, id string) (*Tunnel, error) {
	res, err := c.api.ZeroTrust.Tunnels.Cloudflared.Get(ctx, id, zero_trust.TunnelCloudflaredGetParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		return nil, fmt.Errorf("get tunnel %s: %w", id, err)
	}
	if !res.DeletedAt.IsZero() {
		return nil, fmt.Errorf("get tunnel %s: tunnel was deleted at %s", id, res.DeletedAt)
	}
	t := fromTunnel(*res)
	return &t, nil
}

// FindTunnelByName returns the non-deleted tunnel with that exact name, or nil.
func (c *Client) FindTunnelByName(ctx context.Context, name string) (*Tunnel, error) {
	page, err := c.api.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cf.F(c.AccountID),
		Name:      cf.F(name),
		IsDeleted: cf.F(false),
	})
	if err != nil {
		return nil, fmt.Errorf("list tunnels: %w", err)
	}
	for _, t := range page.Result {
		if t.Name == name && t.DeletedAt.IsZero() {
			tt := fromTunnel(t)
			return &tt, nil
		}
	}
	return nil, nil
}

// CreateTunnel creates a remotely-managed tunnel with a random secret.
func (c *Client) CreateTunnel(ctx context.Context, name string) (*Tunnel, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate tunnel secret: %w", err)
	}
	res, err := c.api.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID:    cf.F(c.AccountID),
		Name:         cf.F(name),
		ConfigSrc:    cf.F(zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare),
		TunnelSecret: cf.F(base64.StdEncoding.EncodeToString(secret)),
	})
	if err != nil {
		return nil, fmt.Errorf("create tunnel %q: %w", name, err)
	}
	t := fromTunnel(*res)
	return &t, nil
}

// DeleteTunnel removes a tunnel, cleaning up stale connections first. A missing tunnel is not an error.
func (c *Client) DeleteTunnel(ctx context.Context, id string) error {
	_, err := c.api.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, id, zero_trust.TunnelCloudflaredConnectionDeleteParams{AccountID: cf.F(c.AccountID)})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("clean up tunnel %s connections: %w", id, err)
	}
	_, err = c.api.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, id, zero_trust.TunnelCloudflaredDeleteParams{AccountID: cf.F(c.AccountID)})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete tunnel %s: %w", id, err)
	}
	return nil
}

// GetTunnelToken returns the connector token used by `cloudflared tunnel run --token`.
func (c *Client) GetTunnelToken(ctx context.Context, id string) (string, error) {
	res, err := c.api.ZeroTrust.Tunnels.Cloudflared.Token.Get(ctx, id, zero_trust.TunnelCloudflaredTokenGetParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		return "", fmt.Errorf("get tunnel %s token: %w", id, err)
	}
	if res == nil {
		return "", fmt.Errorf("get tunnel %s token: empty response", id)
	}
	return *res, nil
}

// OriginRequest is the subset of cloudflared origin settings the operator manages.
// Pointers distinguish "unset" from zero values.
type OriginRequest struct {
	NoTLSVerify            *bool
	HTTPHostHeader         string
	OriginServerName       string
	ConnectTimeout         *int
	TLSTimeout             *int
	TCPKeepAlive           *int
	KeepAliveConnections   *int
	KeepAliveTimeout       *int
	DisableChunkedEncoding *bool
	HTTP2Origin            *bool
	NoHappyEyeballs        *bool
	ProxyType              string
	CAPool                 string
}

// IsZero reports whether no setting is set.
func (o *OriginRequest) IsZero() bool {
	return o == nil || *o == OriginRequest{}
}

// IngressRule is one public hostname rule of a tunnel configuration.
type IngressRule struct {
	Hostname      string
	Path          string
	Service       string
	OriginRequest *OriginRequest
}

// TunnelConfig is the remotely-managed configuration of a tunnel.
type TunnelConfig struct {
	Ingress       []IngressRule
	OriginRequest *OriginRequest
	Version       int64
}

// SortIngress orders rules deterministically: specific paths before catch-alls, hostnames alphabetical,
// and the rule without hostname (catch-all) last.
func SortIngress(rules []IngressRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if (a.Hostname == "") != (b.Hostname == "") {
			return a.Hostname != ""
		}
		if a.Hostname != b.Hostname {
			return a.Hostname < b.Hostname
		}
		if (a.Path == "") != (b.Path == "") {
			return a.Path != ""
		}
		return a.Path < b.Path
	})
}

// Equal compares two configurations ignoring the version.
func (t TunnelConfig) Equal(o TunnelConfig) bool {
	if len(t.Ingress) != len(o.Ingress) {
		return false
	}
	for i := range t.Ingress {
		a, b := t.Ingress[i], o.Ingress[i]
		if a.Hostname != b.Hostname || a.Path != b.Path || a.Service != b.Service || !originEqual(a.OriginRequest, b.OriginRequest) {
			return false
		}
	}
	return originEqual(t.OriginRequest, o.OriginRequest)
}

func originEqual(a, b *OriginRequest) bool {
	if a.IsZero() && b.IsZero() {
		return true
	}
	if a.IsZero() != b.IsZero() {
		return false
	}
	return ptrBoolEq(a.NoTLSVerify, b.NoTLSVerify) && a.HTTPHostHeader == b.HTTPHostHeader &&
		a.OriginServerName == b.OriginServerName && ptrIntEq(a.ConnectTimeout, b.ConnectTimeout) &&
		ptrIntEq(a.TLSTimeout, b.TLSTimeout) && ptrIntEq(a.TCPKeepAlive, b.TCPKeepAlive) &&
		ptrIntEq(a.KeepAliveConnections, b.KeepAliveConnections) && ptrIntEq(a.KeepAliveTimeout, b.KeepAliveTimeout) &&
		ptrBoolEq(a.DisableChunkedEncoding, b.DisableChunkedEncoding) && ptrBoolEq(a.HTTP2Origin, b.HTTP2Origin) &&
		ptrBoolEq(a.NoHappyEyeballs, b.NoHappyEyeballs) && a.ProxyType == b.ProxyType && a.CAPool == b.CAPool
}

func ptrBoolEq(a, b *bool) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func ptrIntEq(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func intPtr(v int64, set bool) *int {
	if !set {
		return nil
	}
	i := int(v)
	return &i
}

func boolPtr(v, set bool) *bool {
	if !set {
		return nil
	}
	return &v
}

// GetTunnelConfig fetches the remote configuration. A tunnel that never had one returns an empty config.
func (c *Client) GetTunnelConfig(ctx context.Context, id string) (*TunnelConfig, error) {
	res, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, id, zero_trust.TunnelCloudflaredConfigurationGetParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		return nil, fmt.Errorf("get tunnel %s configuration: %w", id, err)
	}
	cfg := &TunnelConfig{Version: res.Version}
	for _, in := range res.Config.Ingress {
		rule := IngressRule{Hostname: in.Hostname, Path: in.Path, Service: in.Service}
		o := in.OriginRequest
		rule.OriginRequest = &OriginRequest{
			NoTLSVerify:            boolPtr(o.NoTLSVerify, (!o.JSON.NoTLSVerify.IsMissing() && !o.JSON.NoTLSVerify.IsNull())),
			HTTPHostHeader:         o.HTTPHostHeader,
			OriginServerName:       o.OriginServerName,
			ConnectTimeout:         intPtr(o.ConnectTimeout, (!o.JSON.ConnectTimeout.IsMissing() && !o.JSON.ConnectTimeout.IsNull())),
			TLSTimeout:             intPtr(o.TLSTimeout, (!o.JSON.TLSTimeout.IsMissing() && !o.JSON.TLSTimeout.IsNull())),
			TCPKeepAlive:           intPtr(o.TCPKeepAlive, (!o.JSON.TCPKeepAlive.IsMissing() && !o.JSON.TCPKeepAlive.IsNull())),
			KeepAliveConnections:   intPtr(o.KeepAliveConnections, (!o.JSON.KeepAliveConnections.IsMissing() && !o.JSON.KeepAliveConnections.IsNull())),
			KeepAliveTimeout:       intPtr(o.KeepAliveTimeout, (!o.JSON.KeepAliveTimeout.IsMissing() && !o.JSON.KeepAliveTimeout.IsNull())),
			DisableChunkedEncoding: boolPtr(o.DisableChunkedEncoding, (!o.JSON.DisableChunkedEncoding.IsMissing() && !o.JSON.DisableChunkedEncoding.IsNull())),
			HTTP2Origin:            boolPtr(o.HTTP2Origin, (!o.JSON.HTTP2Origin.IsMissing() && !o.JSON.HTTP2Origin.IsNull())),
			NoHappyEyeballs:        boolPtr(o.NoHappyEyeballs, (!o.JSON.NoHappyEyeballs.IsMissing() && !o.JSON.NoHappyEyeballs.IsNull())),
			ProxyType:              o.ProxyType,
			CAPool:                 o.CAPool,
		}
		if rule.OriginRequest.IsZero() {
			rule.OriginRequest = nil
		}
		cfg.Ingress = append(cfg.Ingress, rule)
	}
	o := res.Config.OriginRequest
	top := &OriginRequest{
		NoTLSVerify:            boolPtr(o.NoTLSVerify, (!o.JSON.NoTLSVerify.IsMissing() && !o.JSON.NoTLSVerify.IsNull())),
		HTTPHostHeader:         o.HTTPHostHeader,
		OriginServerName:       o.OriginServerName,
		ConnectTimeout:         intPtr(o.ConnectTimeout, (!o.JSON.ConnectTimeout.IsMissing() && !o.JSON.ConnectTimeout.IsNull())),
		TLSTimeout:             intPtr(o.TLSTimeout, (!o.JSON.TLSTimeout.IsMissing() && !o.JSON.TLSTimeout.IsNull())),
		TCPKeepAlive:           intPtr(o.TCPKeepAlive, (!o.JSON.TCPKeepAlive.IsMissing() && !o.JSON.TCPKeepAlive.IsNull())),
		KeepAliveConnections:   intPtr(o.KeepAliveConnections, (!o.JSON.KeepAliveConnections.IsMissing() && !o.JSON.KeepAliveConnections.IsNull())),
		KeepAliveTimeout:       intPtr(o.KeepAliveTimeout, (!o.JSON.KeepAliveTimeout.IsMissing() && !o.JSON.KeepAliveTimeout.IsNull())),
		DisableChunkedEncoding: boolPtr(o.DisableChunkedEncoding, (!o.JSON.DisableChunkedEncoding.IsMissing() && !o.JSON.DisableChunkedEncoding.IsNull())),
		HTTP2Origin:            boolPtr(o.HTTP2Origin, (!o.JSON.HTTP2Origin.IsMissing() && !o.JSON.HTTP2Origin.IsNull())),
		NoHappyEyeballs:        boolPtr(o.NoHappyEyeballs, (!o.JSON.NoHappyEyeballs.IsMissing() && !o.JSON.NoHappyEyeballs.IsNull())),
		ProxyType:              o.ProxyType,
		CAPool:                 o.CAPool,
	}
	if !top.IsZero() {
		cfg.OriginRequest = top
	}
	return cfg, nil
}

func toIngressOriginRequest(o *OriginRequest) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	p := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
	if o.NoTLSVerify != nil {
		p.NoTLSVerify = cf.F(*o.NoTLSVerify)
	}
	if o.HTTPHostHeader != "" {
		p.HTTPHostHeader = cf.F(o.HTTPHostHeader)
	}
	if o.OriginServerName != "" {
		p.OriginServerName = cf.F(o.OriginServerName)
	}
	if o.ConnectTimeout != nil {
		p.ConnectTimeout = cf.F(int64(*o.ConnectTimeout))
	}
	if o.TLSTimeout != nil {
		p.TLSTimeout = cf.F(int64(*o.TLSTimeout))
	}
	if o.TCPKeepAlive != nil {
		p.TCPKeepAlive = cf.F(int64(*o.TCPKeepAlive))
	}
	if o.KeepAliveConnections != nil {
		p.KeepAliveConnections = cf.F(int64(*o.KeepAliveConnections))
	}
	if o.KeepAliveTimeout != nil {
		p.KeepAliveTimeout = cf.F(int64(*o.KeepAliveTimeout))
	}
	if o.DisableChunkedEncoding != nil {
		p.DisableChunkedEncoding = cf.F(*o.DisableChunkedEncoding)
	}
	if o.HTTP2Origin != nil {
		p.HTTP2Origin = cf.F(*o.HTTP2Origin)
	}
	if o.NoHappyEyeballs != nil {
		p.NoHappyEyeballs = cf.F(*o.NoHappyEyeballs)
	}
	if o.ProxyType != "" {
		p.ProxyType = cf.F(o.ProxyType)
	}
	if o.CAPool != "" {
		p.CAPool = cf.F(o.CAPool)
	}
	return p
}

func toTopOriginRequest(o *OriginRequest) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigOriginRequest {
	p := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigOriginRequest{}
	if o.NoTLSVerify != nil {
		p.NoTLSVerify = cf.F(*o.NoTLSVerify)
	}
	if o.HTTPHostHeader != "" {
		p.HTTPHostHeader = cf.F(o.HTTPHostHeader)
	}
	if o.OriginServerName != "" {
		p.OriginServerName = cf.F(o.OriginServerName)
	}
	if o.ConnectTimeout != nil {
		p.ConnectTimeout = cf.F(int64(*o.ConnectTimeout))
	}
	if o.TLSTimeout != nil {
		p.TLSTimeout = cf.F(int64(*o.TLSTimeout))
	}
	if o.TCPKeepAlive != nil {
		p.TCPKeepAlive = cf.F(int64(*o.TCPKeepAlive))
	}
	if o.KeepAliveConnections != nil {
		p.KeepAliveConnections = cf.F(int64(*o.KeepAliveConnections))
	}
	if o.KeepAliveTimeout != nil {
		p.KeepAliveTimeout = cf.F(int64(*o.KeepAliveTimeout))
	}
	if o.DisableChunkedEncoding != nil {
		p.DisableChunkedEncoding = cf.F(*o.DisableChunkedEncoding)
	}
	if o.HTTP2Origin != nil {
		p.HTTP2Origin = cf.F(*o.HTTP2Origin)
	}
	if o.NoHappyEyeballs != nil {
		p.NoHappyEyeballs = cf.F(*o.NoHappyEyeballs)
	}
	if o.ProxyType != "" {
		p.ProxyType = cf.F(o.ProxyType)
	}
	if o.CAPool != "" {
		p.CAPool = cf.F(o.CAPool)
	}
	return p
}

// UpdateTunnelConfig replaces the remote configuration and returns the new version.
func (c *Client) UpdateTunnelConfig(ctx context.Context, id string, cfg TunnelConfig) (int64, error) {
	ingress := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(cfg.Ingress))
	for _, r := range cfg.Ingress {
		in := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			Service: cf.F(r.Service),
		}
		if r.Hostname != "" {
			in.Hostname = cf.F(r.Hostname)
		}
		if r.Path != "" {
			in.Path = cf.F(r.Path)
		}
		if !r.OriginRequest.IsZero() {
			in.OriginRequest = cf.F(toIngressOriginRequest(r.OriginRequest))
		}
		ingress = append(ingress, in)
	}
	params := zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cf.F(c.AccountID),
		Config: cf.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
			Ingress: cf.F(ingress),
		}),
	}
	if !cfg.OriginRequest.IsZero() {
		conf := params.Config.Value
		conf.OriginRequest = cf.F(toTopOriginRequest(cfg.OriginRequest))
		params.Config = cf.F(conf)
	}
	res, err := c.api.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, id, params)
	if err != nil {
		return 0, fmt.Errorf("update tunnel %s configuration: %w", id, err)
	}
	return res.Version, nil
}

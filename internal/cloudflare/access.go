package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/zero_trust"
)

// Access applications and groups are driven through the SDK's generic request path with explicit
// JSON structs. The generated typed params for inline application policies do not expose the
// name/decision/include fields, and the response types are unions that are awkward to read back.
// Everything else in this file uses the typed SDK services.

// AccessRule is one Cloudflare Access rule, already resolved to IDs. The JSON shape mirrors the API:
// exactly one key is set per rule.
type AccessRule map[string]any

// Rule constructors. Each returns a single-key rule object as the Access API expects.

func RuleEmail(email string) AccessRule {
	return AccessRule{"email": map[string]string{"email": email}}
}
func RuleEmailDomain(domain string) AccessRule {
	return AccessRule{"email_domain": map[string]string{"domain": domain}}
}
func RuleEveryone() AccessRule      { return AccessRule{"everyone": map[string]string{}} }
func RuleIP(cidr string) AccessRule { return AccessRule{"ip": map[string]string{"ip": cidr}} }
func RuleCountry(cc string) AccessRule {
	return AccessRule{"geo": map[string]string{"country_code": cc}}
}
func RuleLoginMethod(idpID string) AccessRule {
	return AccessRule{"login_method": map[string]string{"id": idpID}}
}
func RuleGroup(groupID string) AccessRule {
	return AccessRule{"group": map[string]string{"id": groupID}}
}
func RuleServiceToken(tokenID string) AccessRule {
	return AccessRule{"service_token": map[string]string{"token_id": tokenID}}
}
func RuleAnyValidServiceToken() AccessRule {
	return AccessRule{"any_valid_service_token": map[string]string{}}
}
func RuleGitHubOrganization(idpID, org, team string) AccessRule {
	m := map[string]string{"identity_provider_id": idpID, "name": org}
	if team != "" {
		m["team"] = team
	}
	return AccessRule{"github-organization": m}
}

// AccessPolicy is an application-scoped policy.
type AccessPolicy struct {
	ID                           string       `json:"id,omitempty"`
	Name                         string       `json:"name"`
	Decision                     string       `json:"decision"`
	Precedence                   int          `json:"precedence"`
	Include                      []AccessRule `json:"include"`
	Exclude                      []AccessRule `json:"exclude"`
	Require                      []AccessRule `json:"require"`
	SessionDuration              string       `json:"session_duration,omitempty"`
	PurposeJustificationRequired bool         `json:"purpose_justification_required,omitempty"`
	PurposeJustificationPrompt   string       `json:"purpose_justification_prompt,omitempty"`
}

// CORSHeaders mirrors the Access cors_headers object.
type CORSHeaders struct {
	AllowAllHeaders  bool     `json:"allow_all_headers,omitempty"`
	AllowAllMethods  bool     `json:"allow_all_methods,omitempty"`
	AllowAllOrigins  bool     `json:"allow_all_origins,omitempty"`
	AllowCredentials bool     `json:"allow_credentials,omitempty"`
	AllowedHeaders   []string `json:"allowed_headers,omitempty"`
	AllowedMethods   []string `json:"allowed_methods,omitempty"`
	AllowedOrigins   []string `json:"allowed_origins,omitempty"`
	MaxAge           *int     `json:"max_age,omitempty"`
}

// AccessApplication is a self-hosted Access application with inline policies.
type AccessApplication struct {
	ID                      string         `json:"id,omitempty"`
	AUD                     string         `json:"aud,omitempty"`
	Type                    string         `json:"type"`
	Name                    string         `json:"name"`
	Domain                  string         `json:"domain"`
	SelfHostedDomains       []string       `json:"self_hosted_domains,omitempty"`
	SessionDuration         string         `json:"session_duration,omitempty"`
	AllowedIdps             []string       `json:"allowed_idps"`
	AutoRedirectToIdentity  bool           `json:"auto_redirect_to_identity"`
	AppLauncherVisible      *bool          `json:"app_launcher_visible,omitempty"`
	SkipInterstitial        bool           `json:"skip_interstitial"`
	ServiceAuth401Redirect  bool           `json:"service_auth_401_redirect"`
	HTTPOnlyCookieAttribute *bool          `json:"http_only_cookie_attribute,omitempty"`
	CustomDenyMessage       string         `json:"custom_deny_message,omitempty"`
	CustomDenyURL           string         `json:"custom_deny_url,omitempty"`
	LogoURL                 string         `json:"logo_url,omitempty"`
	CORSHeaders             *CORSHeaders   `json:"cors_headers,omitempty"`
	Policies                []AccessPolicy `json:"policies"`
}

// Equal reports whether the remote application b already matches the desired application a.
// IDs and server-only fields are ignored, and settings the desired state leaves unset (nil
// pointers, empty session duration) accept whatever default Cloudflare applied.
func (a AccessApplication) Equal(b AccessApplication) bool {
	na, nb := a, b
	na.ID, nb.ID, na.AUD, nb.AUD = "", "", "", ""
	na.Type, nb.Type = "", ""
	if na.SessionDuration == "" {
		na.SessionDuration = nb.SessionDuration
	}
	if na.AppLauncherVisible == nil {
		na.AppLauncherVisible = nb.AppLauncherVisible
	}
	if na.HTTPOnlyCookieAttribute == nil {
		na.HTTPOnlyCookieAttribute = nb.HTTPOnlyCookieAttribute
	}
	if na.CORSHeaders == nil {
		na.CORSHeaders = nb.CORSHeaders
	}
	na.Policies, nb.Policies = normalizePolicies(a.Policies), normalizePolicies(b.Policies)
	if len(na.AllowedIdps) == 0 {
		na.AllowedIdps = nil
	}
	if len(nb.AllowedIdps) == 0 {
		nb.AllowedIdps = nil
	}
	if len(na.SelfHostedDomains) == 0 {
		na.SelfHostedDomains = nil
	}
	if len(nb.SelfHostedDomains) == 0 {
		nb.SelfHostedDomains = nil
	}
	return reflect.DeepEqual(na, nb)
}

func normalizePolicies(ps []AccessPolicy) []AccessPolicy {
	out := make([]AccessPolicy, len(ps))
	for i, p := range ps {
		p.ID = ""
		p.Precedence = i + 1
		p.Include = normalizeRules(p.Include)
		p.Exclude = normalizeRules(p.Exclude)
		p.Require = normalizeRules(p.Require)
		out[i] = p
	}
	return out
}

// normalizeRules round-trips rules through JSON so map value types compare equal regardless of
// whether they came from constructors (map[string]string) or the API (map[string]any).
func normalizeRules(rules []AccessRule) []AccessRule {
	if len(rules) == 0 {
		return nil
	}
	raw, _ := json.Marshal(rules)
	var out []AccessRule
	_ = json.Unmarshal(raw, &out)
	return out
}

type envelope[T any] struct {
	Success bool `json:"success"`
	Result  T    `json:"result"`
}

func (c *Client) accessPath(parts ...string) string {
	p := "accounts/" + c.AccountID + "/access"
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p
}

// GetAccessApplication fetches an application. Returns nil when it does not exist.
func (c *Client) GetAccessApplication(ctx context.Context, id string) (*AccessApplication, error) {
	var env envelope[AccessApplication]
	if err := c.api.Get(ctx, c.accessPath("apps", id), nil, &env); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get access application %s: %w", id, err)
	}
	return &env.Result, nil
}

// FindAccessApplicationByDomain returns the application protecting exactly this domain, or nil.
func (c *Client) FindAccessApplicationByDomain(ctx context.Context, domain string) (*AccessApplication, error) {
	var env envelope[[]AccessApplication]
	q := url.Values{"domain": {domain}, "exact": {"true"}, "per_page": {"50"}}
	if err := c.api.Get(ctx, c.accessPath("apps")+"?"+q.Encode(), nil, &env); err != nil {
		return nil, fmt.Errorf("list access applications: %w", err)
	}
	for _, app := range env.Result {
		if app.Domain == domain {
			a := app
			return &a, nil
		}
	}
	return nil, nil
}

// CreateAccessApplication creates an application with inline policies.
func (c *Client) CreateAccessApplication(ctx context.Context, app AccessApplication) (*AccessApplication, error) {
	app.Type = "self_hosted"
	var env envelope[AccessApplication]
	if err := c.api.Post(ctx, c.accessPath("apps"), app, &env); err != nil {
		return nil, fmt.Errorf("create access application %q: %w", app.Name, err)
	}
	return &env.Result, nil
}

// UpdateAccessApplication replaces an application and its inline policies.
func (c *Client) UpdateAccessApplication(ctx context.Context, app AccessApplication) (*AccessApplication, error) {
	app.Type = "self_hosted"
	id := app.ID
	app.ID = ""
	var env envelope[AccessApplication]
	if err := c.api.Put(ctx, c.accessPath("apps", id), app, &env); err != nil {
		return nil, fmt.Errorf("update access application %s: %w", id, err)
	}
	return &env.Result, nil
}

// DeleteAccessApplication deletes an application. A missing application is not an error.
func (c *Client) DeleteAccessApplication(ctx context.Context, id string) error {
	if err := c.api.Delete(ctx, c.accessPath("apps", id), nil, nil); err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete access application %s: %w", id, err)
	}
	return nil
}

// AccessGroup is a reusable Access group.
type AccessGroup struct {
	ID      string       `json:"id,omitempty"`
	Name    string       `json:"name"`
	Include []AccessRule `json:"include"`
	Exclude []AccessRule `json:"exclude"`
	Require []AccessRule `json:"require"`
}

// Equal compares the managed fields.
func (g AccessGroup) Equal(o AccessGroup) bool {
	return g.Name == o.Name &&
		reflect.DeepEqual(normalizeRules(g.Include), normalizeRules(o.Include)) &&
		reflect.DeepEqual(normalizeRules(g.Exclude), normalizeRules(o.Exclude)) &&
		reflect.DeepEqual(normalizeRules(g.Require), normalizeRules(o.Require))
}

// GetAccessGroup fetches a group. Returns nil when it does not exist.
func (c *Client) GetAccessGroup(ctx context.Context, id string) (*AccessGroup, error) {
	var env envelope[AccessGroup]
	if err := c.api.Get(ctx, c.accessPath("groups", id), nil, &env); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get access group %s: %w", id, err)
	}
	return &env.Result, nil
}

// FindAccessGroupByName returns the group with exactly this name, or nil.
func (c *Client) FindAccessGroupByName(ctx context.Context, name string) (*AccessGroup, error) {
	var env envelope[[]AccessGroup]
	q := url.Values{"name": {name}, "per_page": {"50"}}
	if err := c.api.Get(ctx, c.accessPath("groups")+"?"+q.Encode(), nil, &env); err != nil {
		return nil, fmt.Errorf("list access groups: %w", err)
	}
	for _, g := range env.Result {
		if g.Name == name {
			gg := g
			return &gg, nil
		}
	}
	return nil, nil
}

// CreateAccessGroup creates a group.
func (c *Client) CreateAccessGroup(ctx context.Context, g AccessGroup) (*AccessGroup, error) {
	var env envelope[AccessGroup]
	if err := c.api.Post(ctx, c.accessPath("groups"), g, &env); err != nil {
		return nil, fmt.Errorf("create access group %q: %w", g.Name, err)
	}
	return &env.Result, nil
}

// UpdateAccessGroup replaces a group.
func (c *Client) UpdateAccessGroup(ctx context.Context, g AccessGroup) (*AccessGroup, error) {
	id := g.ID
	g.ID = ""
	var env envelope[AccessGroup]
	if err := c.api.Put(ctx, c.accessPath("groups", id), g, &env); err != nil {
		return nil, fmt.Errorf("update access group %s: %w", id, err)
	}
	return &env.Result, nil
}

// DeleteAccessGroup deletes a group. A missing group is not an error.
func (c *Client) DeleteAccessGroup(ctx context.Context, id string) error {
	if err := c.api.Delete(ctx, c.accessPath("groups", id), nil, nil); err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete access group %s: %w", id, err)
	}
	return nil
}

// ServiceToken is an Access service token. ClientSecret is only set right after creation or rotation.
type ServiceToken struct {
	ID           string
	Name         string
	ClientID     string
	ClientSecret string
	Duration     string
	ExpiresAt    time.Time
}

func fromServiceToken(t zero_trust.ServiceToken) ServiceToken {
	return ServiceToken{ID: t.ID, Name: t.Name, ClientID: t.ClientID, Duration: t.Duration, ExpiresAt: t.ExpiresAt}
}

// GetServiceToken fetches a service token. Returns nil when it does not exist.
func (c *Client) GetServiceToken(ctx context.Context, id string) (*ServiceToken, error) {
	res, err := c.api.ZeroTrust.Access.ServiceTokens.Get(ctx, id, zero_trust.AccessServiceTokenGetParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get service token %s: %w", id, err)
	}
	t := fromServiceToken(*res)
	return &t, nil
}

// FindServiceTokenByName returns the service token with exactly this name, or nil.
func (c *Client) FindServiceTokenByName(ctx context.Context, name string) (*ServiceToken, error) {
	page, err := c.api.ZeroTrust.Access.ServiceTokens.List(ctx, zero_trust.AccessServiceTokenListParams{AccountID: cf.F(c.AccountID), Name: cf.F(name)})
	if err != nil {
		return nil, fmt.Errorf("list service tokens: %w", err)
	}
	for _, t := range page.Result {
		if t.Name == name {
			tt := fromServiceToken(t)
			return &tt, nil
		}
	}
	return nil, nil
}

// CreateServiceToken creates a token. The returned ClientSecret is shown only once.
func (c *Client) CreateServiceToken(ctx context.Context, name, duration string) (*ServiceToken, error) {
	params := zero_trust.AccessServiceTokenNewParams{AccountID: cf.F(c.AccountID), Name: cf.F(name)}
	if duration != "" {
		params.Duration = cf.F(duration)
	}
	res, err := c.api.ZeroTrust.Access.ServiceTokens.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create service token %q: %w", name, err)
	}
	t := &ServiceToken{ID: res.ID, Name: res.Name, ClientID: res.ClientID, ClientSecret: res.ClientSecret, Duration: res.Duration}
	c.fillServiceTokenExpiry(ctx, t)
	return t, nil
}

// fillServiceTokenExpiry fetches the expiry, which create/rotate responses do not carry.
func (c *Client) fillServiceTokenExpiry(ctx context.Context, t *ServiceToken) {
	if got, err := c.GetServiceToken(ctx, t.ID); err == nil && got != nil {
		t.ExpiresAt = got.ExpiresAt
		if t.Duration == "" {
			t.Duration = got.Duration
		}
	}
}

// RotateServiceToken generates a new client secret.
func (c *Client) RotateServiceToken(ctx context.Context, id string) (*ServiceToken, error) {
	res, err := c.api.ZeroTrust.Access.ServiceTokens.Rotate(ctx, id, zero_trust.AccessServiceTokenRotateParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		return nil, fmt.Errorf("rotate service token %s: %w", id, err)
	}
	t := &ServiceToken{ID: res.ID, Name: res.Name, ClientID: res.ClientID, ClientSecret: res.ClientSecret, Duration: res.Duration}
	c.fillServiceTokenExpiry(ctx, t)
	return t, nil
}

// RefreshServiceToken extends the expiry of a token without changing its secret.
func (c *Client) RefreshServiceToken(ctx context.Context, id string) (*ServiceToken, error) {
	res, err := c.api.ZeroTrust.Access.ServiceTokens.Refresh(ctx, id, zero_trust.AccessServiceTokenRefreshParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		return nil, fmt.Errorf("refresh service token %s: %w", id, err)
	}
	t := fromServiceToken(*res)
	return &t, nil
}

// DeleteServiceToken deletes a token. A missing token is not an error.
func (c *Client) DeleteServiceToken(ctx context.Context, id string) error {
	_, err := c.api.ZeroTrust.Access.ServiceTokens.Delete(ctx, id, zero_trust.AccessServiceTokenDeleteParams{AccountID: cf.F(c.AccountID)})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete service token %s: %w", id, err)
	}
	return nil
}

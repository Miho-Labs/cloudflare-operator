package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
)

// Ruleset phases the operator manages.
const (
	PhaseRateLimit       = "http_ratelimit"
	PhaseFirewallCustom  = "http_request_firewall_custom"
	PhaseFirewallManaged = "http_request_firewall_managed"
)

// Well-known managed ruleset names as returned by the API.
const (
	ManagedRulesetCloudflare  = "Cloudflare Managed Ruleset"
	ManagedRulesetOWASP       = "Cloudflare OWASP Core Ruleset"
	ManagedRulesetCredentials = "Cloudflare Exposed Credentials Check Ruleset"
	ManagedRulesetFree        = "Cloudflare Managed Free Ruleset"
)

// RatelimitParams mirror the ratelimit object of a rule.
type RatelimitParams struct {
	Characteristics    []string `json:"characteristics"`
	Period             int      `json:"period"`
	RequestsPerPeriod  int      `json:"requests_per_period,omitempty"`
	MitigationTimeout  int      `json:"mitigation_timeout,omitempty"`
	CountingExpression string   `json:"counting_expression,omitempty"`
	RequestsToOrigin   bool     `json:"requests_to_origin,omitempty"`
}

// Rule is one rule of a ruleset. ActionParameters is kept generic because its shape depends on
// the action.
type Rule struct {
	ID               string           `json:"id,omitempty"`
	Ref              string           `json:"ref,omitempty"`
	Version          string           `json:"version,omitempty"`
	Description      string           `json:"description"`
	Expression       string           `json:"expression"`
	Action           string           `json:"action"`
	ActionParameters map[string]any   `json:"action_parameters,omitempty"`
	Ratelimit        *RatelimitParams `json:"ratelimit,omitempty"`
	Enabled          *bool            `json:"enabled,omitempty"`
}

// Equal compares the managed fields of two rules, ignoring identity and versions.
func (r Rule) Equal(o Rule) bool {
	if r.Description != o.Description || r.Expression != o.Expression || r.Action != o.Action {
		return false
	}
	if boolOr(r.Enabled, true) != boolOr(o.Enabled, true) {
		return false
	}
	mine, theirs := normalizeJSON(r.ActionParameters), normalizeJSON(o.ActionParameters)
	// The API pins the managed ruleset version it executes; ignore it unless the spec set one.
	if m, ok := mine.(map[string]any); ok {
		if t, ok := theirs.(map[string]any); ok && m["version"] == nil {
			delete(t, "version")
		}
	}
	if !looseEqual(mine, theirs) {
		return false
	}
	return looseEqual(normalizeJSON(r.Ratelimit), normalizeJSON(o.Ratelimit))
}

// looseEqual compares decoded JSON values, treating a key that is missing on one side as equal
// to a zero value (false, "", 0, null, empty) on the other. The API echoes defaults the operator
// omits, and those must not count as drift.
func looseEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return isZeroJSON(a) && isZeroJSON(b)
		}
		for k, x := range av {
			y, present := bv[k]
			if !present {
				if !isZeroJSON(x) {
					return false
				}
				continue
			}
			if !looseEqual(x, y) {
				return false
			}
		}
		for k, y := range bv {
			if _, present := av[k]; !present && !isZeroJSON(y) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return isZeroJSON(a) && isZeroJSON(b)
		}
		for i := range av {
			if !looseEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	}
	if a == nil || b == nil {
		return isZeroJSON(a) && isZeroJSON(b)
	}
	return reflect.DeepEqual(a, b)
}

func isZeroJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case float64:
		return x == 0
	case map[string]any:
		for _, y := range x {
			if !isZeroJSON(y) {
				return false
			}
		}
		return true
	case []any:
		return len(x) == 0
	}
	return false
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// normalizeJSON round-trips a value through JSON so map/number types compare equal regardless of
// whether they came from Go structs or decoded API responses. nil and empty collapse to nil.
func normalizeJSON(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// Ruleset is a zone ruleset (phase entrypoint or managed ruleset).
type Ruleset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Phase       string `json:"phase"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
}

// FindRuleByDescription returns the first rule with that description, or nil.
func (rs *Ruleset) FindRuleByDescription(desc string) *Rule {
	if rs == nil || desc == "" {
		return nil
	}
	for i := range rs.Rules {
		if rs.Rules[i].Description == desc {
			return &rs.Rules[i]
		}
	}
	return nil
}

// FindRuleByID returns the rule with that ID, or nil.
func (rs *Ruleset) FindRuleByID(id string) *Rule {
	if rs == nil || id == "" {
		return nil
	}
	for i := range rs.Rules {
		if rs.Rules[i].ID == id {
			return &rs.Rules[i]
		}
	}
	return nil
}

// RulePosition places a rule when adding or updating it.
type RulePosition struct {
	Index  int    `json:"index,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

func (c *Client) zoneRulesetsPath(zoneID string, parts ...string) string {
	p := "zones/" + url.PathEscape(zoneID) + "/rulesets"
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p
}

// GetPhaseEntrypoint returns the zone's entrypoint ruleset for a phase, or nil when none exists yet.
func (c *Client) GetPhaseEntrypoint(ctx context.Context, zoneID, phase string) (*Ruleset, error) {
	var env envelope[Ruleset]
	if err := c.api.Get(ctx, c.zoneRulesetsPath(zoneID, "phases", phase, "entrypoint"), nil, &env); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get %s entrypoint ruleset: %w", phase, err)
	}
	return &env.Result, nil
}

// EnsurePhaseEntrypoint returns the entrypoint ruleset for a phase, creating an empty one if needed.
func (c *Client) EnsurePhaseEntrypoint(ctx context.Context, zoneID, phase string) (*Ruleset, error) {
	rs, err := c.GetPhaseEntrypoint(ctx, zoneID, phase)
	if err != nil || rs != nil {
		return rs, err
	}
	body := map[string]any{
		"name":        "default",
		"description": "managed by cloudflare-operator",
		"kind":        "zone",
		"phase":       phase,
		"rules":       []any{},
	}
	var env envelope[Ruleset]
	if err := c.api.Post(ctx, c.zoneRulesetsPath(zoneID), body, &env); err != nil {
		return nil, fmt.Errorf("create %s entrypoint ruleset: %w", phase, err)
	}
	return &env.Result, nil
}

// GetRuleset fetches a ruleset by ID.
func (c *Client) GetRuleset(ctx context.Context, zoneID, rulesetID string) (*Ruleset, error) {
	var env envelope[Ruleset]
	if err := c.api.Get(ctx, c.zoneRulesetsPath(zoneID, rulesetID), nil, &env); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get ruleset %s: %w", rulesetID, err)
	}
	return &env.Result, nil
}

// ListManagedRulesets lists the Cloudflare managed rulesets available to the zone.
func (c *Client) ListManagedRulesets(ctx context.Context, zoneID string) ([]Ruleset, error) {
	var env envelope[[]Ruleset]
	if err := c.api.Get(ctx, c.zoneRulesetsPath(zoneID), nil, &env); err != nil {
		return nil, fmt.Errorf("list rulesets: %w", err)
	}
	var out []Ruleset
	for _, rs := range env.Result {
		if rs.Kind == "managed" {
			out = append(out, rs)
		}
	}
	return out, nil
}

// AddRule appends (or inserts, when position is set) a rule and returns it with its ID.
func (c *Client) AddRule(ctx context.Context, zoneID, rulesetID string, rule Rule, position *RulePosition) (*Rule, error) {
	body := map[string]any{
		"description": rule.Description,
		"expression":  rule.Expression,
		"action":      rule.Action,
		"enabled":     boolOr(rule.Enabled, true),
	}
	if rule.Ref != "" {
		body["ref"] = rule.Ref
	}
	if len(rule.ActionParameters) > 0 {
		body["action_parameters"] = rule.ActionParameters
	}
	if rule.Ratelimit != nil {
		body["ratelimit"] = rule.Ratelimit
	}
	if position != nil && (position.Index > 0 || position.Before != "" || position.After != "") {
		body["position"] = position
	}
	var env envelope[Ruleset]
	if err := c.api.Post(ctx, c.zoneRulesetsPath(zoneID, rulesetID, "rules"), body, &env); err != nil {
		return nil, fmt.Errorf("add rule %q: %w", rule.Description, err)
	}
	// The response is the whole ruleset; find our rule by ref or description.
	if rule.Ref != "" {
		for i := range env.Result.Rules {
			if env.Result.Rules[i].Ref == rule.Ref {
				return &env.Result.Rules[i], nil
			}
		}
	}
	if r := env.Result.FindRuleByDescription(rule.Description); r != nil {
		return r, nil
	}
	return nil, fmt.Errorf("add rule %q: rule not found in response", rule.Description)
}

// UpdateRule replaces a rule's fields (PATCH) and optionally moves it.
func (c *Client) UpdateRule(ctx context.Context, zoneID, rulesetID, ruleID string, rule Rule, position *RulePosition) (*Rule, error) {
	body := map[string]any{
		"description": rule.Description,
		"expression":  rule.Expression,
		"action":      rule.Action,
		"enabled":     boolOr(rule.Enabled, true),
	}
	if rule.Ref != "" {
		body["ref"] = rule.Ref
	}
	if len(rule.ActionParameters) > 0 {
		body["action_parameters"] = rule.ActionParameters
	}
	if rule.Ratelimit != nil {
		body["ratelimit"] = rule.Ratelimit
	}
	if position != nil && (position.Index > 0 || position.Before != "" || position.After != "") {
		body["position"] = position
	}
	var env envelope[Ruleset]
	if err := c.api.Patch(ctx, c.zoneRulesetsPath(zoneID, rulesetID, "rules", ruleID), body, &env); err != nil {
		return nil, fmt.Errorf("update rule %s: %w", ruleID, err)
	}
	if r := env.Result.FindRuleByID(ruleID); r != nil {
		return r, nil
	}
	if r := env.Result.FindRuleByDescription(rule.Description); r != nil {
		return r, nil
	}
	return nil, fmt.Errorf("update rule %s: rule not found in response", ruleID)
}

// DeleteRule removes a rule. A missing rule or ruleset is not an error.
func (c *Client) DeleteRule(ctx context.Context, zoneID, rulesetID, ruleID string) error {
	if err := c.api.Delete(ctx, c.zoneRulesetsPath(zoneID, rulesetID, "rules", ruleID), nil, nil); err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete rule %s: %w", ruleID, err)
	}
	return nil
}

// RuleIndex returns the 1-based position of a rule in the ruleset, or 0.
func (rs *Ruleset) RuleIndex(ruleID string) int {
	if rs == nil {
		return 0
	}
	for i := range rs.Rules {
		if rs.Rules[i].ID == ruleID {
			return i + 1
		}
	}
	return 0
}

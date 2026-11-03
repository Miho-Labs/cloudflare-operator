package cloudflare

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// IPAccessRule is a firewall access rule at zone or account level.
type IPAccessRule struct {
	ID     string `json:"id,omitempty"`
	Mode   string `json:"mode"`
	Target string `json:"-"`
	Value  string `json:"-"`
	Notes  string `json:"notes,omitempty"`
}

type ipAccessRuleJSON struct {
	ID            string `json:"id,omitempty"`
	Mode          string `json:"mode"`
	Notes         string `json:"notes,omitempty"`
	Configuration struct {
		Target string `json:"target"`
		Value  string `json:"value"`
	} `json:"configuration"`
}

func (r ipAccessRuleJSON) toRule() IPAccessRule {
	return IPAccessRule{ID: r.ID, Mode: r.Mode, Notes: r.Notes, Target: r.Configuration.Target, Value: r.Configuration.Value}
}

// Equal compares the managed fields.
func (r IPAccessRule) Equal(o IPAccessRule) bool {
	return r.Mode == o.Mode && r.Notes == o.Notes && r.Target == o.Target && strings.EqualFold(r.Value, o.Value)
}

// IPAccessScope selects where an access rule lives: a zone ID, or "" for the account.
type IPAccessScope string

func (c *Client) accessRulesPath(scope IPAccessScope, parts ...string) string {
	var p string
	if scope == "" {
		p = "accounts/" + c.AccountID + "/firewall/access_rules/rules"
	} else {
		p = "zones/" + url.PathEscape(string(scope)) + "/firewall/access_rules/rules"
	}
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p
}

// GetIPAccessRule fetches a rule. Returns nil when it does not exist.
func (c *Client) GetIPAccessRule(ctx context.Context, scope IPAccessScope, id string) (*IPAccessRule, error) {
	var env envelope[ipAccessRuleJSON]
	if err := c.api.Get(ctx, c.accessRulesPath(scope, id), nil, &env); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get access rule %s: %w", id, err)
	}
	r := env.Result.toRule()
	return &r, nil
}

// FindIPAccessRule returns the rule with that target and value, or nil.
func (c *Client) FindIPAccessRule(ctx context.Context, scope IPAccessScope, target, value string) (*IPAccessRule, error) {
	var env envelope[[]ipAccessRuleJSON]
	q := url.Values{"configuration.target": {target}, "configuration.value": {value}, "per_page": {"100"}}
	if err := c.api.Get(ctx, c.accessRulesPath(scope)+"?"+q.Encode(), nil, &env); err != nil {
		return nil, fmt.Errorf("list access rules: %w", err)
	}
	for _, r := range env.Result {
		if r.Configuration.Target == target && strings.EqualFold(r.Configuration.Value, value) {
			rr := r.toRule()
			return &rr, nil
		}
	}
	return nil, nil
}

// CreateIPAccessRule creates a rule.
func (c *Client) CreateIPAccessRule(ctx context.Context, scope IPAccessScope, r IPAccessRule) (*IPAccessRule, error) {
	body := map[string]any{
		"mode":          r.Mode,
		"notes":         r.Notes,
		"configuration": map[string]string{"target": r.Target, "value": r.Value},
	}
	var env envelope[ipAccessRuleJSON]
	if err := c.api.Post(ctx, c.accessRulesPath(scope), body, &env); err != nil {
		return nil, fmt.Errorf("create access rule %s %s: %w", r.Target, r.Value, err)
	}
	out := env.Result.toRule()
	return &out, nil
}

// UpdateIPAccessRule changes mode and notes of a rule (target and value are immutable).
func (c *Client) UpdateIPAccessRule(ctx context.Context, scope IPAccessScope, r IPAccessRule) (*IPAccessRule, error) {
	body := map[string]any{"mode": r.Mode, "notes": r.Notes}
	var env envelope[ipAccessRuleJSON]
	if err := c.api.Patch(ctx, c.accessRulesPath(scope, r.ID), body, &env); err != nil {
		return nil, fmt.Errorf("update access rule %s: %w", r.ID, err)
	}
	out := env.Result.toRule()
	return &out, nil
}

// DeleteIPAccessRule deletes a rule. A missing rule is not an error.
func (c *Client) DeleteIPAccessRule(ctx context.Context, scope IPAccessScope, id string) error {
	if err := c.api.Delete(ctx, c.accessRulesPath(scope, id), nil, nil); err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete access rule %s: %w", id, err)
	}
	return nil
}

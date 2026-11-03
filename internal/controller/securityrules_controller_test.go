package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare/fake"
)

// entrypoint returns the fake zone ruleset for a phase, or nil.
func (e *env) entrypoint(zoneID, phase string) *fake.Ruleset {
	e.t.Helper()
	for _, rs := range e.cf.Rulesets {
		if rs.ZoneID == zoneID && rs.Kind == "zone" && rs.Phase == phase {
			rs := rs
			return &rs
		}
	}
	return nil
}

// seedEntrypoint creates a zone ruleset for a phase with the given rules.
func (e *env) seedEntrypoint(zoneID, phase string, rules ...map[string]any) string {
	e.t.Helper()
	id := "rs-" + phase
	for _, r := range rules {
		if r["version"] == nil {
			r["version"] = "1"
		}
	}
	e.cf.Rulesets[id] = fake.Ruleset{ID: id, ZoneID: zoneID, Name: "default", Kind: "zone", Phase: phase, Version: "1", Rules: rules}
	return id
}

func ruleByID(rs *fake.Ruleset, id string) map[string]any {
	if rs == nil {
		return nil
	}
	for _, r := range rs.Rules {
		if r["id"] == id {
			return r
		}
	}
	return nil
}

func rateLimitRule(name string) *v1alpha1.RateLimitRule {
	return &v1alpha1.RateLimitRule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.RateLimitRuleSpec{
			AccountRef:  v1alpha1.AccountRef{Name: testAccount},
			Zone:        v1alpha1.ZoneRef{Name: "example.com"},
			Description: "login throttle",
			Expression:  `(http.request.uri.path eq "/login")`,
			RateLimit:   v1alpha1.RateLimitParameters{RequestsPerPeriod: 20},
		},
	}
}

func TestRateLimitRuleLifecycle(t *testing.T) {
	obj := rateLimitRule("login")
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	r := &RateLimitRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}

	// First pass adds the finalizer only.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	e.get(obj)
	if len(obj.Finalizers) != 1 {
		t.Fatalf("finalizer missing: %v", obj.Finalizers)
	}
	// Second pass creates the entrypoint and the rule.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	if obj.Status.ZoneID != zoneID || obj.Status.RulesetID == "" || obj.Status.RuleID == "" || obj.Status.RuleVersion != "1" {
		t.Fatalf("status: %+v", obj.Status)
	}
	rs := e.entrypoint(zoneID, cloudflare.PhaseRateLimit)
	if rs == nil || rs.ID != obj.Status.RulesetID || len(rs.Rules) != 1 {
		t.Fatalf("entrypoint: %+v", rs)
	}
	rule := ruleByID(rs, obj.Status.RuleID)
	rl, _ := rule["ratelimit"].(map[string]any)
	if rule["action"] != "block" || rule["enabled"] != true || rl == nil || rl["period"] != float64(10) ||
		rl["requests_per_period"] != float64(20) || rl["mitigation_timeout"] != float64(10) {
		t.Fatalf("remote rule: %+v", rule)
	}
	if chars, _ := rl["characteristics"].([]any); len(chars) != 2 || chars[0] != "ip.src" || chars[1] != "cf.colo.id" {
		t.Fatalf("default characteristics: %+v", rl["characteristics"])
	}

	// Unchanged spec: no PATCH.
	before := e.cf.CallCount("PATCH ")
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("PATCH ") != before {
		t.Fatal("unexpected update on unchanged spec")
	}

	// Spec change: block response and a longer period.
	e.get(obj)
	obj.Spec.RateLimit.Period = 60
	obj.Spec.RateLimit.MitigationTimeout = 600
	obj.Spec.BlockResponse = &v1alpha1.BlockResponse{StatusCode: 429, Body: "slow down"}
	if err := e.k8s.Update(e.ctx, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	e.get(obj)
	rule = ruleByID(e.entrypoint(zoneID, cloudflare.PhaseRateLimit), obj.Status.RuleID)
	rl, _ = rule["ratelimit"].(map[string]any)
	ap, _ := rule["action_parameters"].(map[string]any)
	resp, _ := ap["response"].(map[string]any)
	if rl["period"] != float64(60) || rl["mitigation_timeout"] != float64(600) || resp["status_code"] != float64(429) ||
		resp["content"] != "slow down" || resp["content_type"] != "text/plain" {
		t.Fatalf("remote rule after update: %+v", rule)
	}
	if obj.Status.RuleVersion != "2" {
		t.Fatalf("rule version: %+v", obj.Status)
	}

	// Drift on Cloudflare is corrected.
	rs = e.entrypoint(zoneID, cloudflare.PhaseRateLimit)
	ruleByID(rs, obj.Status.RuleID)["expression"] = "true"
	e.cf.Rulesets[rs.ID] = *rs
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	if got := ruleByID(e.entrypoint(zoneID, cloudflare.PhaseRateLimit), obj.Status.RuleID); got["expression"] != obj.Spec.Expression {
		t.Fatalf("drift not corrected: %+v", got)
	}

	// Rule removed out of band is recreated.
	rs = e.entrypoint(zoneID, cloudflare.PhaseRateLimit)
	rs.Rules = nil
	e.cf.Rulesets[rs.ID] = *rs
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	if rs = e.entrypoint(zoneID, cloudflare.PhaseRateLimit); len(rs.Rules) != 1 || rs.Rules[0]["id"] != obj.Status.RuleID {
		t.Fatalf("rule not recreated: %+v / %+v", rs.Rules, obj.Status)
	}

	// Delete removes the rule and leaves the (empty) entrypoint.
	e.delete(obj)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	if rs = e.entrypoint(zoneID, cloudflare.PhaseRateLimit); rs == nil || len(rs.Rules) != 0 {
		t.Fatalf("rule not deleted: %+v", rs)
	}
	if e.exists(obj) {
		t.Fatal("object should be gone after finalizer removal")
	}
}

func TestRateLimitRuleRetain(t *testing.T) {
	obj := rateLimitRule("login")
	obj.Spec.DeletionPolicy = v1alpha1.DeletionPolicyRetain
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	r := &RateLimitRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	e.delete(obj)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "login")); err != nil {
		t.Fatal(err)
	}
	if rs := e.entrypoint(zoneID, cloudflare.PhaseRateLimit); len(rs.Rules) != 1 {
		t.Fatalf("rule should be retained: %+v", rs.Rules)
	}
	if e.exists(obj) {
		t.Fatal("object should be gone")
	}
}

func wafRule(name string) *v1alpha1.WAFRule {
	return &v1alpha1.WAFRule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.WAFRuleSpec{
			AccountRef:  v1alpha1.AccountRef{Name: testAccount},
			Zone:        v1alpha1.ZoneRef{Name: "example.com"},
			Description: "block admin from outside",
			Expression:  `(http.request.uri.path contains "/admin" and not ip.geoip.country eq "PL")`,
		},
	}
}

func TestWAFRuleAdoptsAndMoves(t *testing.T) {
	obj := wafRule("admin")
	obj.Spec.Action = "managed_challenge"
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	e.seedEntrypoint(zoneID, cloudflare.PhaseFirewallCustom,
		map[string]any{"id": "first", "description": "dashboard rule", "expression": "true", "action": "log", "enabled": true},
		map[string]any{"id": "mine", "description": "block admin from outside", "expression": "true", "action": "block", "enabled": true},
	)
	r := &WAFRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	if obj.Status.RuleID != "mine" || obj.Status.RulesetID != "rs-"+cloudflare.PhaseFirewallCustom {
		t.Fatalf("expected adoption, got %+v", obj.Status)
	}
	if e.cf.CallCount("POST ") != 0 {
		t.Fatal("adoption must not create rules or rulesets")
	}
	rs := e.entrypoint(zoneID, cloudflare.PhaseFirewallCustom)
	if len(rs.Rules) != 2 || rs.Rules[1]["id"] != "mine" {
		t.Fatalf("ruleset after adoption: %+v", rs.Rules)
	}
	if got := rs.Rules[1]; got["action"] != "managed_challenge" || got["expression"] != obj.Spec.Expression {
		t.Fatalf("adopted rule not updated: %+v", got)
	}

	// Position moves the rule to the front; a second pass is a no-op.
	e.get(obj)
	obj.Spec.Position = &v1alpha1.RulePosition{Index: ptr(1)}
	if err := e.k8s.Update(e.ctx, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
		t.Fatal(err)
	}
	rs = e.entrypoint(zoneID, cloudflare.PhaseFirewallCustom)
	if rs.Rules[0]["id"] != "mine" || rs.Rules[1]["id"] != "first" {
		t.Fatalf("rule not moved: %+v", rs.Rules)
	}
	before := e.cf.CallCount("PATCH ")
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("PATCH ") != before {
		t.Fatal("unexpected update when already in position")
	}

	// Switching to skip replaces the action parameters.
	e.get(obj)
	obj.Spec.Action = "skip"
	obj.Spec.Skip = &v1alpha1.SkipParameters{Products: []string{"waf", "rateLimit"}}
	if err := e.k8s.Update(e.ctx, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
		t.Fatal(err)
	}
	got := ruleByID(e.entrypoint(zoneID, cloudflare.PhaseFirewallCustom), "mine")
	ap, _ := got["action_parameters"].(map[string]any)
	if got["action"] != "skip" || ap == nil || ap["ruleset"] != nil {
		t.Fatalf("skip rule: %+v", got)
	}
	if products, _ := ap["products"].([]any); len(products) != 2 || products[1] != "rateLimit" {
		t.Fatalf("skip products: %+v", ap)
	}
}

func TestWAFRuleDisabledAndDeleted(t *testing.T) {
	obj := wafRule("admin")
	obj.Spec.Enabled = ptr(false)
	obj.Spec.BlockResponse = &v1alpha1.BlockResponse{StatusCode: 403, ContentType: "application/json", Body: `{"error":"no"}`}
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	r := &WAFRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	rule := ruleByID(e.entrypoint(zoneID, cloudflare.PhaseFirewallCustom), obj.Status.RuleID)
	resp, _ := rule["action_parameters"].(map[string]any)["response"].(map[string]any)
	if rule["enabled"] != false || rule["action"] != "block" || resp["content_type"] != "application/json" {
		t.Fatalf("remote rule: %+v", rule)
	}
	e.delete(obj)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
		t.Fatal(err)
	}
	if rs := e.entrypoint(zoneID, cloudflare.PhaseFirewallCustom); len(rs.Rules) != 0 {
		t.Fatalf("rule not deleted: %+v", rs.Rules)
	}
}

func managedWAFRule(name string) *v1alpha1.ManagedWAFRule {
	return &v1alpha1.ManagedWAFRule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.ManagedWAFRuleSpec{
			AccountRef:  v1alpha1.AccountRef{Name: testAccount},
			Zone:        v1alpha1.ZoneRef{Name: "example.com"},
			Description: "owasp",
			Ruleset:     "owasp-core",
			Overrides: &v1alpha1.ManagedRulesetOverrides{
				Action:         "managed_challenge",
				ScoreThreshold: ptr(40),
				ParanoiaLevel:  ptr(2),
				Rules:          []v1alpha1.ManagedRuleOverride{{ID: "abc", Enabled: ptr(false)}},
				Categories:     []v1alpha1.ManagedCategoryOverride{{Category: "wordpress", Action: "log"}},
			},
		},
	}
}

func TestManagedWAFRuleDeploysRuleset(t *testing.T) {
	obj := managedWAFRule("owasp")
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	r := &ManagedWAFRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "owasp")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	if obj.Status.ManagedRulesetID != "4814384a9e5d4991b9815dcfc25d2f1f" || obj.Status.RuleID == "" {
		t.Fatalf("status: %+v", obj.Status)
	}
	rule := ruleByID(e.entrypoint(zoneID, cloudflare.PhaseFirewallManaged), obj.Status.RuleID)
	ap, _ := rule["action_parameters"].(map[string]any)
	ov, _ := ap["overrides"].(map[string]any)
	if rule["action"] != "execute" || rule["expression"] != "true" || ap["id"] != obj.Status.ManagedRulesetID || ov["action"] != "managed_challenge" {
		t.Fatalf("remote rule: %+v", rule)
	}
	rules, _ := ov["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("rule overrides: %+v", ov)
	}
	if th := rules[1].(map[string]any); th["id"] != owaspEvaluationRuleID || th["score_threshold"] != float64(40) {
		t.Fatalf("score threshold override: %+v", th)
	}
	cats, _ := ov["categories"].([]any)
	var names []string
	for _, c := range cats {
		names = append(names, c.(map[string]any)["category"].(string))
	}
	if strings.Join(names, ",") != "wordpress,paranoia-level-3,paranoia-level-4" {
		t.Fatalf("category overrides: %v", names)
	}

	// Steady state does not patch.
	before := e.cf.CallCount("PATCH ")
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "owasp")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("PATCH ") != before {
		t.Fatal("unexpected update on unchanged spec")
	}

	// Dropping overrides updates the rule.
	e.get(obj)
	obj.Spec.Overrides = nil
	if err := e.k8s.Update(e.ctx, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "owasp")); err != nil {
		t.Fatal(err)
	}
	rule = ruleByID(e.entrypoint(zoneID, cloudflare.PhaseFirewallManaged), obj.Status.RuleID)
	if ap, _ := rule["action_parameters"].(map[string]any); ap["overrides"] != nil {
		t.Fatalf("overrides not removed: %+v", rule)
	}

	e.delete(obj)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "owasp")); err != nil {
		t.Fatal(err)
	}
	if rs := e.entrypoint(zoneID, cloudflare.PhaseFirewallManaged); len(rs.Rules) != 0 {
		t.Fatalf("rule not deleted: %+v", rs.Rules)
	}
}

func TestManagedWAFRuleUnavailableRuleset(t *testing.T) {
	obj := managedWAFRule("cf")
	obj.Spec.Ruleset = "cloudflare-managed"
	obj.Spec.Overrides = nil
	e := newEnv(t, obj)
	e.cf.AddZone("example.com")
	delete(e.cf.Rulesets, "efb7b8c949ac4650a09736fc376e9aee")
	r := &ManagedWAFRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "cf")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, false)
	if c := readyCondition(obj.Status.Conditions); c.Reason != v1alpha1.ReasonInvalidSpec || !strings.Contains(c.Message, "not available") {
		t.Fatalf("condition: %+v", c)
	}
}

func ipAccessRule(name string) *v1alpha1.IPAccessRule {
	return &v1alpha1.IPAccessRule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.IPAccessRuleSpec{
			AccountRef: v1alpha1.AccountRef{Name: testAccount},
			Zone:       &v1alpha1.ZoneRef{Name: "example.com"},
			Mode:       "block",
			Target:     "ip",
			Value:      "198.51.100.7",
			Notes:      "abusive scanner",
		},
	}
}

func TestIPAccessRuleLifecycle(t *testing.T) {
	obj := ipAccessRule("scanner")
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	r := &IPAccessRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "scanner")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	if obj.Status.ZoneID != zoneID || obj.Status.RuleID == "" {
		t.Fatalf("status: %+v", obj.Status)
	}
	got := e.cf.AccessRules[obj.Status.RuleID]
	if got.Scope != zoneID || got.Mode != "block" || got.Notes != "abusive scanner" || got.Configuration["value"] != "198.51.100.7" {
		t.Fatalf("remote rule: %+v", got)
	}

	// Mode/notes change is a PATCH in place.
	e.get(obj)
	obj.Spec.Mode = "managed_challenge"
	if err := e.k8s.Update(e.ctx, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "scanner")); err != nil {
		t.Fatal(err)
	}
	if got := e.cf.AccessRules[obj.Status.RuleID]; got.Mode != "managed_challenge" || len(e.cf.AccessRules) != 1 {
		t.Fatalf("remote rule after update: %+v", got)
	}

	// Value change replaces the rule.
	e.get(obj)
	oldID := obj.Status.RuleID
	obj.Spec.Target = "ip_range"
	obj.Spec.Value = "198.51.100.0/24"
	if err := e.k8s.Update(e.ctx, obj); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "scanner")); err != nil {
		t.Fatal(err)
	}
	e.get(obj)
	if _, ok := e.cf.AccessRules[oldID]; ok || obj.Status.RuleID == oldID || len(e.cf.AccessRules) != 1 {
		t.Fatalf("rule not replaced: %+v / %+v", obj.Status, e.cf.AccessRules)
	}
	if got := e.cf.AccessRules[obj.Status.RuleID]; got.Configuration["target"] != "ip_range" || got.Mode != "managed_challenge" {
		t.Fatalf("replacement rule: %+v", got)
	}

	e.delete(obj)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "scanner")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.AccessRules) != 0 || e.exists(obj) {
		t.Fatal("rule not deleted")
	}
}

func TestIPAccessRuleAdoptsExisting(t *testing.T) {
	obj := ipAccessRule("scanner")
	e := newEnv(t, obj)
	zoneID := e.cf.AddZone("example.com")
	e.cf.AccessRules["pre"] = fake.AccessRule{ID: "pre", Scope: zoneID, Mode: "challenge", Configuration: map[string]any{"target": "ip", "value": "198.51.100.7"}}
	r := &IPAccessRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "scanner")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	if obj.Status.RuleID != "pre" || len(e.cf.AccessRules) != 1 {
		t.Fatalf("expected adoption, got %+v (%d rules)", obj.Status, len(e.cf.AccessRules))
	}
	if got := e.cf.AccessRules["pre"]; got.Mode != "block" || got.Notes != "abusive scanner" {
		t.Fatalf("adopted rule not updated: %+v", got)
	}
}

func TestIPAccessRuleAccountScope(t *testing.T) {
	obj := ipAccessRule("country")
	obj.Spec.Scope = "account"
	obj.Spec.Zone = nil
	obj.Spec.Target = "country"
	obj.Spec.Value = "KP"
	obj.Spec.Mode = "whitelist"
	e := newEnv(t, obj)
	r := &IPAccessRuleReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, "country")); err != nil {
			t.Fatal(err)
		}
	}
	e.get(obj)
	assertReady(t, obj.Status.Conditions, true)
	got := e.cf.AccessRules[obj.Status.RuleID]
	if got.Scope != "" || got.Mode != "whitelist" || got.Configuration["target"] != "country" || obj.Status.ZoneID != "" {
		t.Fatalf("account rule: %+v / %+v", got, obj.Status)
	}
	if e.cf.CallCount("POST /zones/") != 0 {
		t.Fatal("account-scoped rule must not touch zone endpoints")
	}
	e.delete(obj)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "country")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.AccessRules) != 0 {
		t.Fatal("rule not deleted")
	}
}

package fake

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Ruleset is a fake zone or managed ruleset.
type Ruleset struct {
	ID          string           `json:"id"`
	ZoneID      string           `json:"-"`
	Name        string           `json:"name"`
	Kind        string           `json:"kind"`
	Phase       string           `json:"phase"`
	Version     string           `json:"version"`
	Description string           `json:"description,omitempty"`
	Rules       []map[string]any `json:"rules"`
}

// AccessRule is a fake IP access rule.
type AccessRule struct {
	ID            string         `json:"id"`
	Scope         string         `json:"-"` // zone ID or "" for account
	Mode          string         `json:"mode"`
	Notes         string         `json:"notes"`
	Configuration map[string]any `json:"configuration"`
}

// SeedManagedRulesets registers the managed rulesets every zone can see.
func (s *Server) seedManagedRulesets() {
	for id, name := range map[string]string{
		"efb7b8c949ac4650a09736fc376e9aee": "Cloudflare Managed Ruleset",
		"4814384a9e5d4991b9815dcfc25d2f1f": "Cloudflare OWASP Core Ruleset",
		"c2e184081120413c86c3ab7e14069605": "Cloudflare Exposed Credentials Check Ruleset",
		"77454fe2d30c4220b5701f6fdfb893ba": "Cloudflare Managed Free Ruleset",
	} {
		s.Rulesets[id] = Ruleset{ID: id, Name: name, Kind: "managed", Phase: "http_request_firewall_managed", Version: "latest", Rules: []map[string]any{}}
	}
}

func (s *Server) rulesetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /zones/{zone}/rulesets", s.listRulesets)
	mux.HandleFunc("POST /zones/{zone}/rulesets", s.createRuleset)
	mux.HandleFunc("GET /zones/{zone}/rulesets/phases/{phase}/entrypoint", s.getEntrypoint)
	mux.HandleFunc("GET /zones/{zone}/rulesets/{id}", s.getRuleset)
	mux.HandleFunc("POST /zones/{zone}/rulesets/{id}/rules", s.addRule)
	mux.HandleFunc("PATCH /zones/{zone}/rulesets/{id}/rules/{rule}", s.updateRule)
	mux.HandleFunc("DELETE /zones/{zone}/rulesets/{id}/rules/{rule}", s.deleteRule)

	mux.HandleFunc("GET /zones/{zone}/firewall/access_rules/rules", s.listAccessRules)
	mux.HandleFunc("POST /zones/{zone}/firewall/access_rules/rules", s.createAccessRule)
	mux.HandleFunc("GET /zones/{zone}/firewall/access_rules/rules/{id}", s.getAccessRule)
	mux.HandleFunc("PATCH /zones/{zone}/firewall/access_rules/rules/{id}", s.updateAccessRule)
	mux.HandleFunc("DELETE /zones/{zone}/firewall/access_rules/rules/{id}", s.deleteAccessRule)
	mux.HandleFunc("GET /accounts/{account}/firewall/access_rules/rules", s.listAccessRules)
	mux.HandleFunc("POST /accounts/{account}/firewall/access_rules/rules", s.createAccessRule)
	mux.HandleFunc("GET /accounts/{account}/firewall/access_rules/rules/{id}", s.getAccessRule)
	mux.HandleFunc("PATCH /accounts/{account}/firewall/access_rules/rules/{id}", s.updateAccessRule)
	mux.HandleFunc("DELETE /accounts/{account}/firewall/access_rules/rules/{id}", s.deleteAccessRule)
}

func bump(v string) string {
	switch v {
	case "", "latest":
		return "1"
	}
	n := 0
	for _, ch := range v {
		n = n*10 + int(ch-'0')
	}
	return itoa(n + 1)
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func (s *Server) listRulesets(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	zone := r.PathValue("zone")
	if _, ok := s.Zones[zone]; !ok {
		notFound(w, "zone")
		return
	}
	out := []Ruleset{}
	for _, rs := range s.Rulesets {
		if rs.Kind == "managed" || rs.ZoneID == zone {
			out = append(out, rs)
		}
	}
	writeList(w, out, len(out))
}

func (s *Server) createRuleset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	zone := r.PathValue("zone")
	if _, ok := s.Zones[zone]; !ok {
		notFound(w, "zone")
		return
	}
	var rs Ruleset
	if err := decode(r, &rs); err != nil || rs.Phase == "" {
		writeError(w, 400, 20000, "invalid ruleset")
		return
	}
	for _, existing := range s.Rulesets {
		if existing.ZoneID == zone && existing.Kind == "zone" && existing.Phase == rs.Phase {
			writeError(w, 400, 20021, "a zone ruleset already exists for this phase")
			return
		}
	}
	rs.ID, rs.ZoneID, rs.Kind, rs.Version = newID(), zone, "zone", "1"
	if rs.Rules == nil {
		rs.Rules = []map[string]any{}
	}
	for _, rule := range rs.Rules {
		rule["id"] = newID()
		rule["version"] = "1"
	}
	s.Rulesets[rs.ID] = rs
	writeJSON(w, 200, rs)
}

func (s *Server) entrypointLocked(zone, phase string) (Ruleset, bool) {
	for _, rs := range s.Rulesets {
		if rs.ZoneID == zone && rs.Kind == "zone" && rs.Phase == phase {
			return rs, true
		}
	}
	return Ruleset{}, false
}

func (s *Server) getEntrypoint(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.entrypointLocked(r.PathValue("zone"), r.PathValue("phase"))
	if !ok {
		notFound(w, "ruleset")
		return
	}
	writeJSON(w, 200, rs)
}

func (s *Server) getRuleset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.Rulesets[r.PathValue("id")]
	if !ok || (rs.Kind != "managed" && rs.ZoneID != r.PathValue("zone")) {
		notFound(w, "ruleset")
		return
	}
	writeJSON(w, 200, rs)
}

func validateRule(rule map[string]any) (int, string) {
	if rule["expression"] == nil || rule["expression"] == "" {
		return 20102, "expression is required"
	}
	if rule["action"] == nil || rule["action"] == "" {
		return 20103, "action is required"
	}
	if rule["action"] == "execute" {
		ap, _ := rule["action_parameters"].(map[string]any)
		if ap == nil || ap["id"] == nil {
			return 20104, "execute requires action_parameters.id"
		}
	}
	return 0, ""
}

func (s *Server) addRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.Rulesets[r.PathValue("id")]
	if !ok || rs.ZoneID != r.PathValue("zone") {
		notFound(w, "ruleset")
		return
	}
	var rule map[string]any
	if err := decode(r, &rule); err != nil {
		writeError(w, 400, 20000, err.Error())
		return
	}
	if code, msg := validateRule(rule); code != 0 {
		writeError(w, 400, code, msg)
		return
	}
	if rs.Phase == "http_ratelimit" {
		if rl, _ := rule["ratelimit"].(map[string]any); rl == nil || rl["period"] == nil {
			writeError(w, 400, 20105, "ratelimit.period is required")
			return
		}
	}
	rule["id"] = newID()
	rule["version"] = "1"
	pos, _ := rule["position"].(map[string]any)
	delete(rule, "position")
	rs.Rules = insertRule(rs.Rules, rule, pos)
	rs.Version = bump(rs.Version)
	s.Rulesets[rs.ID] = rs
	writeJSON(w, 200, rs)
}

func insertRule(rules []map[string]any, rule map[string]any, pos map[string]any) []map[string]any {
	idx := len(rules)
	if pos != nil {
		if i, ok := pos["index"].(float64); ok && int(i) >= 1 && int(i) <= len(rules)+1 {
			idx = int(i) - 1
		}
		if before, ok := pos["before"].(string); ok {
			for i, rr := range rules {
				if rr["id"] == before {
					idx = i
				}
			}
		}
		if after, ok := pos["after"].(string); ok {
			for i, rr := range rules {
				if rr["id"] == after {
					idx = i + 1
				}
			}
		}
	}
	out := append([]map[string]any{}, rules[:idx]...)
	out = append(out, rule)
	return append(out, rules[idx:]...)
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.Rulesets[r.PathValue("id")]
	if !ok || rs.ZoneID != r.PathValue("zone") {
		notFound(w, "ruleset")
		return
	}
	ruleID := r.PathValue("rule")
	var patch map[string]any
	if err := decode(r, &patch); err != nil {
		writeError(w, 400, 20000, err.Error())
		return
	}
	idx := -1
	for i, rule := range rs.Rules {
		if rule["id"] == ruleID {
			idx = i
		}
	}
	if idx < 0 {
		notFound(w, "rule")
		return
	}
	// The rule PATCH endpoint replaces the rule definition: fields omitted from the body are
	// dropped, only the identity survives.
	merged := map[string]any{"id": ruleID}
	for k, v := range patch {
		merged[k] = v
	}
	if ref, ok := rs.Rules[idx]["ref"]; ok && merged["ref"] == nil {
		merged["ref"] = ref
	}
	if code, msg := validateRule(merged); code != 0 {
		writeError(w, 400, code, msg)
		return
	}
	merged["version"] = bump(rs.Rules[idx]["version"].(string))
	pos, _ := merged["position"].(map[string]any)
	delete(merged, "position")
	rules := append([]map[string]any{}, rs.Rules[:idx]...)
	rules = append(rules, rs.Rules[idx+1:]...)
	if pos == nil {
		pos = map[string]any{"index": float64(idx + 1)}
	}
	rs.Rules = insertRule(rules, merged, pos)
	rs.Version = bump(rs.Version)
	s.Rulesets[rs.ID] = rs
	writeJSON(w, 200, rs)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.Rulesets[r.PathValue("id")]
	if !ok || rs.ZoneID != r.PathValue("zone") {
		notFound(w, "ruleset")
		return
	}
	ruleID := r.PathValue("rule")
	kept := []map[string]any{}
	found := false
	for _, rule := range rs.Rules {
		if rule["id"] == ruleID {
			found = true
			continue
		}
		kept = append(kept, rule)
	}
	if !found {
		notFound(w, "rule")
		return
	}
	rs.Rules = kept
	rs.Version = bump(rs.Version)
	s.Rulesets[rs.ID] = rs
	writeJSON(w, 200, rs)
}

// --- IP access rules -------------------------------------------------------

func accessScope(r *http.Request) string {
	if z := r.PathValue("zone"); z != "" {
		return z
	}
	return ""
}

func (s *Server) listAccessRules(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := accessScope(r)
	if scope != "" {
		if _, ok := s.Zones[scope]; !ok {
			notFound(w, "zone")
			return
		}
	}
	q := r.URL.Query()
	out := []AccessRule{}
	for _, ar := range s.AccessRules {
		if ar.Scope != scope {
			continue
		}
		if t := q.Get("configuration.target"); t != "" && ar.Configuration["target"] != t {
			continue
		}
		if v := q.Get("configuration.value"); v != "" && !strings.EqualFold(ar.Configuration["value"].(string), v) {
			continue
		}
		out = append(out, ar)
	}
	writeList(w, out, len(out))
}

func (s *Server) createAccessRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := accessScope(r)
	var ar AccessRule
	if err := decode(r, &ar); err != nil || ar.Mode == "" || ar.Configuration["target"] == nil || ar.Configuration["value"] == nil {
		writeError(w, 400, 10000, "invalid body")
		return
	}
	for _, existing := range s.AccessRules {
		if existing.Scope == scope && existing.Configuration["target"] == ar.Configuration["target"] && strings.EqualFold(existing.Configuration["value"].(string), ar.Configuration["value"].(string)) {
			writeError(w, 400, 10009, "firewallaccessrules.api.duplicate_of_existing")
			return
		}
	}
	ar.ID, ar.Scope = newID(), scope
	s.AccessRules[ar.ID] = ar
	writeJSON(w, 200, ar)
}

func (s *Server) getAccessRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, ok := s.AccessRules[r.PathValue("id")]
	if !ok || ar.Scope != accessScope(r) {
		notFound(w, "access rule")
		return
	}
	writeJSON(w, 200, ar)
}

func (s *Server) updateAccessRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ar, ok := s.AccessRules[r.PathValue("id")]
	if !ok || ar.Scope != accessScope(r) {
		notFound(w, "access rule")
		return
	}
	var patch struct {
		Mode  *string `json:"mode"`
		Notes *string `json:"notes"`
	}
	if err := decode(r, &patch); err != nil {
		writeError(w, 400, 10000, err.Error())
		return
	}
	if patch.Mode != nil {
		ar.Mode = *patch.Mode
	}
	if patch.Notes != nil {
		ar.Notes = *patch.Notes
	}
	s.AccessRules[ar.ID] = ar
	writeJSON(w, 200, ar)
}

func (s *Server) deleteAccessRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	ar, ok := s.AccessRules[id]
	if !ok || ar.Scope != accessScope(r) {
		notFound(w, "access rule")
		return
	}
	delete(s.AccessRules, id)
	writeJSON(w, 200, map[string]any{"id": id})
}

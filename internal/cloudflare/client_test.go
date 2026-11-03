package cloudflare_test

import (
	"context"
	"strings"
	"testing"

	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare/fake"
)

func newClient(t *testing.T) (*cloudflare.Client, *fake.Server) {
	t.Helper()
	srv := fake.New()
	t.Cleanup(srv.Close)
	c := cloudflare.New(cloudflare.Options{APIToken: srv.Token, AccountID: srv.AccountID, BaseURL: srv.URL})
	return c, srv
}

func TestVerifyAccountAndToken(t *testing.T) {
	c, srv := newClient(t)
	acc, err := c.VerifyAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if acc.Name != srv.AccountName {
		t.Fatalf("got name %q", acc.Name)
	}
	tok, err := c.VerifyToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Status != "active" {
		t.Fatalf("got status %q", tok.Status)
	}

	bad := cloudflare.New(cloudflare.Options{APIToken: "wrong", AccountID: srv.AccountID, BaseURL: srv.URL})
	if _, err := bad.VerifyAccount(context.Background()); !cloudflare.IsAuthError(err) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestZonesAndDNS(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	zoneID := srv.AddZone("example.com")

	z, err := c.ZoneByName(ctx, "example.com")
	if err != nil || z.ID != zoneID {
		t.Fatalf("zone lookup: %v %+v", err, z)
	}
	if _, err := c.ZoneByName(ctx, "nope.com"); err == nil {
		t.Fatal("expected error for unknown zone")
	}
	z, err = c.ZoneForHostname(ctx, "a.b.example.com")
	if err != nil || z.ID != zoneID {
		t.Fatalf("zone for hostname: %v %+v", err, z)
	}

	rec, err := c.CreateDNSRecord(ctx, cloudflare.DNSRecord{ZoneID: zoneID, Type: "CNAME", Name: "app.example.com", Content: "x.cfargotunnel.com", Proxied: true, TTL: 1})
	if err != nil {
		t.Fatal(err)
	}
	found, err := c.FindDNSRecord(ctx, zoneID, "CNAME", "app.example.com", "x.cfargotunnel.com")
	if err != nil || found == nil || found.ID != rec.ID {
		t.Fatalf("find: %v %+v", err, found)
	}
	if !found.Equal(cloudflare.DNSRecord{Type: "cname", Name: "APP.example.com", Content: "x.cfargotunnel.com.", Proxied: true, TTL: 300}) {
		t.Fatal("expected records to compare equal (proxied ignores ttl, cname content case-insensitive)")
	}
	rec.Content = "y.cfargotunnel.com"
	if _, err := c.UpdateDNSRecord(ctx, *rec); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetDNSRecord(ctx, zoneID, rec.ID)
	if err != nil || got.Content != "y.cfargotunnel.com" {
		t.Fatalf("get after update: %v %+v", err, got)
	}
	if err := c.DeleteDNSRecord(ctx, zoneID, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDNSRecord(ctx, zoneID, rec.ID); err != nil {
		t.Fatalf("second delete should be a no-op: %v", err)
	}
}

func TestZoneLookupErrors(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	srv.Zones["other"] = fake.Zone{ID: "other", Name: "other.com", AccountID: "ffffffffffffffffffffffffffffffff"}
	_, err := c.ZoneByName(ctx, "other.com")
	if !cloudflare.IsZoneNotFound(err) || !strings.Contains(err.Error(), "belongs to account ffffffffffffffffffffffffffffffff") {
		t.Fatalf("expected foreign-account error, got %v", err)
	}
	_, err = c.ZoneByName(ctx, "missing.com")
	if !cloudflare.IsZoneNotFound(err) || !strings.Contains(err.Error(), "Zone:Read") {
		t.Fatalf("expected not-visible error, got %v", err)
	}
	_, err = c.ZoneForHostname(ctx, "a.missing.com")
	if !cloudflare.IsZoneNotFound(err) {
		t.Fatalf("expected zone-not-found for hostname, got %v", err)
	}
	srv.Fail["GET /zones"] = 403
	_, err = c.ZoneForHostname(ctx, "a.missing.com")
	if cloudflare.IsZoneNotFound(err) || !cloudflare.IsAuthError(err) {
		t.Fatalf("API errors must not be masked as not-found: %v", err)
	}
}

func TestFindDNSRecordMatchesContent(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	zoneID := srv.AddZone("example.com")
	srv.DNSRecords["spf"] = fake.DNSRecord{ID: "spf", ZoneID: zoneID, Type: "TXT", Name: "example.com", Content: "v=spf1 -all"}
	srv.DNSRecords["gsv"] = fake.DNSRecord{ID: "gsv", ZoneID: zoneID, Type: "TXT", Name: "example.com", Content: "google-site-verification=abc"}
	srv.DNSRecords["mx1"] = fake.DNSRecord{ID: "mx1", ZoneID: zoneID, Type: "MX", Name: "example.com", Content: "mx1.mail.com."}
	srv.DNSRecords["a"] = fake.DNSRecord{ID: "a", ZoneID: zoneID, Type: "A", Name: "app.example.com", Content: "192.0.2.1"}
	srv.DNSRecords["a2"] = fake.DNSRecord{ID: "a2", ZoneID: zoneID, Type: "A", Name: "app.example.com", Content: "192.0.2.2"}
	srv.DNSRecords["cn"] = fake.DNSRecord{ID: "cn", ZoneID: zoneID, Type: "CNAME", Name: "www.example.com", Content: "example.com"}
	srv.DNSRecords["lone"] = fake.DNSRecord{ID: "lone", ZoneID: zoneID, Type: "A", Name: "lone.example.com", Content: "192.0.2.7"}

	cases := []struct {
		typ, name, content, want string
	}{
		{"TXT", "example.com", "google-site-verification=abc", "gsv"}, // content picks the right sibling
		{"TXT", "example.com", "v=spf1 -all", "spf"},
		{"TXT", "example.com", "something-new", ""},  // no match → create, never adopt a sibling
		{"MX", "example.com", "mx1.mail.com", "mx1"}, // trailing dot ignored
		{"MX", "example.com", "mx2.mail.com", ""},
		{"A", "app.example.com", "192.0.2.2", "a2"},
		{"A", "app.example.com", "192.0.2.9", ""},               // several A records exist: create next to them
		{"A", "lone.example.com", "192.0.2.9", "lone"},          // a lone A is taken over
		{"TXT", "example.com", "nope", ""},                      // never take over a TXT/MX by name alone
		{"CNAME", "app.example.com", "x.cfargotunnel.com", "a"}, // CNAME replaces whatever A sits there
		{"A", "www.example.com", "192.0.2.5", "cn"},             // A replaces a CNAME
		{"AAAA", "www.example.com", "2001:db8::1", "cn"},
	}
	for _, tc := range cases {
		got, err := c.FindDNSRecord(ctx, zoneID, tc.typ, tc.name, tc.content)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.typ, tc.name, err)
		}
		gotID := ""
		if got != nil {
			gotID = got.ID
		}
		if gotID != tc.want {
			t.Fatalf("%s %s %q: got %q, want %q", tc.typ, tc.name, tc.content, gotID, tc.want)
		}
	}
	// Converting the A record into a proxied CNAME works in place.
	updated, err := c.UpdateDNSRecord(ctx, cloudflare.DNSRecord{ID: "a", ZoneID: zoneID, Type: "CNAME", Name: "app.example.com", Content: "x.cfargotunnel.com", Proxied: true, TTL: 1})
	if err != nil || updated.Type != "CNAME" || !updated.Proxied {
		t.Fatalf("convert: %+v %v", updated, err)
	}
	// Overwriting a record with a sibling's content is the duplicate error the controller recovers from.
	_, err = c.UpdateDNSRecord(ctx, cloudflare.DNSRecord{ID: "gsv", ZoneID: zoneID, Type: "TXT", Name: "example.com", Content: "v=spf1 -all", TTL: 1})
	if !cloudflare.IsDuplicateRecord(err) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if cloudflare.TruncateComment(strings.Repeat("x", 150)) != strings.Repeat("x", 100) {
		t.Fatal("comment not truncated")
	}
}

func TestTunnels(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	tun, err := c.CreateTunnel(ctx, "k8s")
	if err != nil {
		t.Fatal(err)
	}
	found, err := c.FindTunnelByName(ctx, "k8s")
	if err != nil || found == nil || found.ID != tun.ID {
		t.Fatalf("find: %v %+v", err, found)
	}
	token, err := c.GetTunnelToken(ctx, tun.ID)
	if err != nil || token == "" {
		t.Fatalf("token: %v %q", err, token)
	}
	cfg, err := c.GetTunnelConfig(ctx, tun.ID)
	if err != nil || len(cfg.Ingress) != 0 {
		t.Fatalf("empty config: %v %+v", err, cfg)
	}
	noTLS := true
	want := cloudflare.TunnelConfig{Ingress: []cloudflare.IngressRule{
		{Hostname: "a.example.com", Service: "http://svc:80", OriginRequest: &cloudflare.OriginRequest{NoTLSVerify: &noTLS, HTTPHostHeader: "a"}},
		{Service: "http_status:404"},
	}}
	v, err := c.UpdateTunnelConfig(ctx, tun.ID, want)
	if err != nil || v != 1 {
		t.Fatalf("update: %v version=%d", err, v)
	}
	cfg, err = c.GetTunnelConfig(ctx, tun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Equal(want) {
		t.Fatalf("round trip mismatch: %+v vs %+v", cfg, want)
	}
	if err := c.DeleteTunnel(ctx, tun.ID); err != nil {
		t.Fatal(err)
	}
	if found, _ := c.FindTunnelByName(ctx, "k8s"); found != nil {
		t.Fatal("tunnel should be gone")
	}
}

func TestSortIngress(t *testing.T) {
	rules := []cloudflare.IngressRule{
		{Service: "http_status:404"},
		{Hostname: "b.example.com", Service: "b"},
		{Hostname: "a.example.com", Service: "a"},
		{Hostname: "a.example.com", Path: "/api", Service: "a-api"},
	}
	cloudflare.SortIngress(rules)
	got := []string{}
	for _, r := range rules {
		got = append(got, r.Hostname+r.Path)
	}
	want := []string{"a.example.com/api", "a.example.com", "b.example.com", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func TestR2AndTokens(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	if b, err := c.GetR2Bucket(ctx, "missing", ""); err != nil || b != nil {
		t.Fatalf("missing bucket: %v %+v", err, b)
	}
	b, err := c.CreateR2Bucket(ctx, cloudflare.R2BucketInput{Name: "data", LocationHint: "weur"})
	if err != nil || b.Name != "data" {
		t.Fatalf("create: %v %+v", err, b)
	}
	ids, err := c.PermissionGroupIDs(ctx, cloudflare.PermR2BucketItemRead, cloudflare.PermR2BucketItemWrite)
	if err != nil || len(ids) != 2 {
		t.Fatalf("permission groups: %v %+v", err, ids)
	}
	tok, err := c.CreateAccountToken(ctx, "k8s-data", []cloudflare.TokenPolicy{{
		PermissionGroupIDs: []string{ids[cloudflare.PermR2BucketItemRead]},
		Resources:          map[string]string{c.R2BucketResource("data", ""): "*"},
	}})
	if err != nil || tok.Value == "" {
		t.Fatalf("token: %v %+v", err, tok)
	}
	if exists, err := c.AccountTokenExists(ctx, tok.ID); err != nil || !exists {
		t.Fatalf("exists: %v %v", err, exists)
	}
	if err := c.DeleteAccountToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if exists, _ := c.AccountTokenExists(ctx, tok.ID); exists {
		t.Fatal("token should be gone")
	}
	srv.NonEmptyBucket["data"] = true
	if err := c.DeleteR2Bucket(ctx, "data", ""); !cloudflare.IsConflict(err) {
		t.Fatalf("expected conflict for non-empty bucket, got %v", err)
	}
	srv.NonEmptyBucket["data"] = false
	if err := c.DeleteR2Bucket(ctx, "data", ""); err != nil {
		t.Fatal(err)
	}
	if cloudflare.R2SecretAccessKey("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("sha256 derivation wrong")
	}
}

func TestAccess(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()
	g, err := c.CreateAccessGroup(ctx, cloudflare.AccessGroup{Name: "admins", Include: []cloudflare.AccessRule{cloudflare.RuleEmailDomain("example.com")}})
	if err != nil || g.ID == "" {
		t.Fatalf("group: %v %+v", err, g)
	}
	found, err := c.FindAccessGroupByName(ctx, "admins")
	if err != nil || found == nil || found.ID != g.ID {
		t.Fatalf("find group: %v %+v", err, found)
	}
	if !found.Equal(cloudflare.AccessGroup{Name: "admins", Include: []cloudflare.AccessRule{cloudflare.RuleEmailDomain("example.com")}}) {
		t.Fatal("group should compare equal after round trip")
	}

	st, err := c.CreateServiceToken(ctx, "ci", "720h")
	if err != nil || st.ClientSecret == "" || st.ExpiresAt.IsZero() {
		t.Fatalf("service token: %v %+v", err, st)
	}
	refreshed, err := c.RefreshServiceToken(ctx, st.ID)
	if err != nil || refreshed.ClientSecret != "" {
		t.Fatalf("refresh: %v %+v", err, refreshed)
	}

	app := cloudflare.AccessApplication{
		Name: "app", Domain: "app.example.com", SessionDuration: "24h",
		Policies: []cloudflare.AccessPolicy{{
			Name: "allow", Decision: "allow", Precedence: 1,
			Include: []cloudflare.AccessRule{cloudflare.RuleGroup(g.ID), cloudflare.RuleServiceToken(st.ID)},
		}},
	}
	created, err := c.CreateAccessApplication(ctx, app)
	if err != nil || created.ID == "" || created.AUD == "" {
		t.Fatalf("app: %v %+v", err, created)
	}
	got, err := c.GetAccessApplication(ctx, created.ID)
	if err != nil || got == nil {
		t.Fatalf("get app: %v", err)
	}
	if !got.Equal(app) {
		t.Fatalf("app should compare equal after round trip:\n%+v\n%+v", got, app)
	}
	// Server-applied defaults for settings the spec leaves unset are not drift.
	visible := true
	defaulted := *got
	defaulted.AppLauncherVisible = &visible
	defaulted.HTTPOnlyCookieAttribute = &visible
	unsetApp := app
	unsetApp.SessionDuration = ""
	if !unsetApp.Equal(defaulted) {
		t.Fatal("unset settings must accept server defaults")
	}
	if app.Equal(defaulted) == false {
		t.Fatal("explicit session duration still compared")
	}
	changed := defaulted
	changed.SessionDuration = "1h"
	if app.Equal(changed) {
		t.Fatal("explicit session duration mismatch must be drift")
	}
	explicitHidden := app
	hidden := false
	explicitHidden.AppLauncherVisible = &hidden
	if explicitHidden.Equal(defaulted) {
		t.Fatal("explicit appLauncherVisible mismatch must be drift")
	}
	byDomain, err := c.FindAccessApplicationByDomain(ctx, "app.example.com")
	if err != nil || byDomain == nil || byDomain.ID != created.ID {
		t.Fatalf("find by domain: %v %+v", err, byDomain)
	}
	app.ID = created.ID
	app.Policies[0].Decision = "deny"
	if _, err := c.UpdateAccessApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	got, _ = c.GetAccessApplication(ctx, created.ID)
	if got.Policies[0].Decision != "deny" {
		t.Fatal("update not applied")
	}
	if err := c.DeleteAccessApplication(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAccessGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteServiceToken(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.GetAccessApplication(ctx, created.ID); got != nil {
		t.Fatal("app should be gone")
	}
}

func TestRulesetsAndRules(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	zoneID := srv.AddZone("example.com")

	if rs, err := c.GetPhaseEntrypoint(ctx, zoneID, cloudflare.PhaseRateLimit); err != nil || rs != nil {
		t.Fatalf("no entrypoint expected: %v %+v", err, rs)
	}
	rs, err := c.EnsurePhaseEntrypoint(ctx, zoneID, cloudflare.PhaseRateLimit)
	if err != nil || rs.ID == "" || rs.Phase != cloudflare.PhaseRateLimit {
		t.Fatalf("ensure: %v %+v", err, rs)
	}
	again, err := c.EnsurePhaseEntrypoint(ctx, zoneID, cloudflare.PhaseRateLimit)
	if err != nil || again.ID != rs.ID {
		t.Fatalf("ensure must be idempotent: %v %+v", err, again)
	}

	enabled := true
	first := cloudflare.Rule{Description: "api limit", Expression: `(http.request.uri.path wildcard "/api/*")`, Action: "block",
		Enabled: &enabled, Ratelimit: &cloudflare.RatelimitParams{Characteristics: []string{"ip.src", "cf.colo.id"}, Period: 10, RequestsPerPeriod: 100, MitigationTimeout: 10}}
	r1, err := c.AddRule(ctx, zoneID, rs.ID, first, nil)
	if err != nil || r1.ID == "" {
		t.Fatalf("add: %v %+v", err, r1)
	}
	second := first
	second.Description = "login limit"
	second.Expression = `(http.request.uri.path eq "/login")`
	r2, err := c.AddRule(ctx, zoneID, rs.ID, second, &cloudflare.RulePosition{Index: 1})
	if err != nil {
		t.Fatal(err)
	}
	rs, _ = c.GetPhaseEntrypoint(ctx, zoneID, cloudflare.PhaseRateLimit)
	if rs.RuleIndex(r2.ID) != 1 || rs.RuleIndex(r1.ID) != 2 {
		t.Fatalf("position not honoured: %+v", rs.Rules)
	}
	got := rs.FindRuleByDescription("api limit")
	if got == nil || !got.Equal(first) {
		t.Fatalf("round trip mismatch: %+v vs %+v", got, first)
	}
	first.Ratelimit.RequestsPerPeriod = 50
	if got.Equal(first) {
		t.Fatal("changed ratelimit must not compare equal")
	}
	upd, err := c.UpdateRule(ctx, zoneID, rs.ID, r1.ID, first, nil)
	if err != nil || upd.Ratelimit.RequestsPerPeriod != 50 || upd.ID != r1.ID {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if err := c.DeleteRule(ctx, zoneID, rs.ID, r1.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRule(ctx, zoneID, rs.ID, r1.ID); err != nil {
		t.Fatalf("second delete must be a no-op: %v", err)
	}
	rs, _ = c.GetPhaseEntrypoint(ctx, zoneID, cloudflare.PhaseRateLimit)
	if len(rs.Rules) != 1 {
		t.Fatalf("expected one rule left, got %d", len(rs.Rules))
	}

	managed, err := c.ListManagedRulesets(ctx, zoneID)
	if err != nil || len(managed) != 4 {
		t.Fatalf("managed rulesets: %v %d", err, len(managed))
	}
	// Managed deployment with action parameters survives the round trip.
	mrs, _ := c.EnsurePhaseEntrypoint(ctx, zoneID, cloudflare.PhaseFirewallManaged)
	exec := cloudflare.Rule{Description: "owasp", Expression: "true", Action: "execute", ActionParameters: map[string]any{
		"id": managed[0].ID, "overrides": map[string]any{"action": "log", "categories": []map[string]any{{"category": "wordpress", "enabled": false}}},
	}}
	if _, err := c.AddRule(ctx, zoneID, mrs.ID, exec, nil); err != nil {
		t.Fatal(err)
	}
	mrs, _ = c.GetPhaseEntrypoint(ctx, zoneID, cloudflare.PhaseFirewallManaged)
	if got := mrs.FindRuleByDescription("owasp"); got == nil || !got.Equal(exec) {
		t.Fatalf("execute rule round trip: %+v", got)
	}
}

func TestIPAccessRules(t *testing.T) {
	c, srv := newClient(t)
	ctx := context.Background()
	zoneID := srv.AddZone("example.com")
	scope := cloudflare.IPAccessScope(zoneID)

	created, err := c.CreateIPAccessRule(ctx, scope, cloudflare.IPAccessRule{Mode: "block", Target: "country", Value: "RU", Notes: "no"})
	if err != nil || created.ID == "" || created.Target != "country" {
		t.Fatalf("create: %v %+v", err, created)
	}
	found, err := c.FindIPAccessRule(ctx, scope, "country", "ru")
	if err != nil || found == nil || found.ID != created.ID {
		t.Fatalf("find: %v %+v", err, found)
	}
	if _, err := c.CreateIPAccessRule(ctx, scope, cloudflare.IPAccessRule{Mode: "block", Target: "country", Value: "RU"}); !cloudflare.HasCode(err, 10009) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	created.Mode = "managed_challenge"
	upd, err := c.UpdateIPAccessRule(ctx, scope, *created)
	if err != nil || upd.Mode != "managed_challenge" {
		t.Fatalf("update: %v %+v", err, upd)
	}
	// Account scope is separate from zone scope.
	acct, err := c.CreateIPAccessRule(ctx, "", cloudflare.IPAccessRule{Mode: "whitelist", Target: "ip", Value: "203.0.113.5"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.FindIPAccessRule(ctx, scope, "ip", "203.0.113.5"); got != nil {
		t.Fatal("account rule must not be visible in zone scope")
	}
	if err := c.DeleteIPAccessRule(ctx, "", acct.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteIPAccessRule(ctx, scope, created.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.GetIPAccessRule(ctx, scope, created.ID); got != nil {
		t.Fatal("rule should be gone")
	}
}

func TestRuleEqualIgnoresEchoedDefaults(t *testing.T) {
	desired := cloudflare.Rule{
		Description: "r", Expression: "true", Action: "block",
		Ratelimit: &cloudflare.RatelimitParams{Characteristics: []string{"ip.src", "cf.colo.id"}, Period: 10, RequestsPerPeriod: 5, MitigationTimeout: 10},
	}
	remote := desired
	remote.ID, remote.Version = "abc", "3"
	remote.Enabled = ptrBool(true)
	remote.ActionParameters = map[string]any{"response": map[string]any{"content": "", "status_code": nil}}
	remoteRL := *desired.Ratelimit
	remoteRL.RequestsToOrigin = false
	remoteRL.CountingExpression = ""
	remote.Ratelimit = &remoteRL
	if !desired.Equal(remote) {
		t.Fatal("echoed zero-value fields must not count as drift")
	}
	changed := remote
	changed.ActionParameters = map[string]any{"response": map[string]any{"status_code": float64(429)}}
	if desired.Equal(changed) {
		t.Fatal("a real difference must be detected")
	}
	changedRL := *desired.Ratelimit
	changedRL.RequestsToOrigin = true
	changed = remote
	changed.Ratelimit = &changedRL
	if desired.Equal(changed) {
		t.Fatal("requests_to_origin true vs absent must be detected")
	}
	managed := cloudflare.Rule{Description: "m", Expression: "true", Action: "execute",
		ActionParameters: map[string]any{"id": "x", "overrides": map[string]any{"categories": []map[string]any{{"category": "paranoia-level-3", "enabled": false}}}}}
	managedRemote := managed
	managedRemote.ActionParameters = map[string]any{"id": "x", "version": "latest", "overrides": map[string]any{"categories": []map[string]any{{"category": "paranoia-level-3", "enabled": false}}}}
	if !managed.Equal(managedRemote) {
		t.Fatal("server-added version must not count as drift")
	}
	managedRemote.ActionParameters = map[string]any{"id": "x", "overrides": map[string]any{"categories": []map[string]any{{"category": "paranoia-level-3", "enabled": true}}}}
	if managed.Equal(managedRemote) {
		t.Fatal("enabled flip must be detected")
	}
}

func ptrBool(b bool) *bool { return &b }

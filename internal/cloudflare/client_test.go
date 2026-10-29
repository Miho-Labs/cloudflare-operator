package cloudflare_test

import (
	"context"
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
	found, err := c.FindDNSRecord(ctx, zoneID, "CNAME", "app.example.com")
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

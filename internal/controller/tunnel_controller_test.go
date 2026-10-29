package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare/fake"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflared"
)

func tunnel(name string) *v1alpha1.Tunnel {
	return &v1alpha1.Tunnel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec:       v1alpha1.TunnelSpec{AccountRef: v1alpha1.AccountRef{Name: testAccount}},
	}
}

func tunnelIngress(name, tunnelName, hostname string) *v1alpha1.TunnelIngress {
	return &v1alpha1.TunnelIngress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.TunnelIngressSpec{
			TunnelRef: v1alpha1.LocalObjectRef{Name: tunnelName},
			Hostname:  hostname,
			Service:   v1alpha1.TunnelIngressService{Name: "web", Port: 8080},
		},
	}
}

func ingressRules(e *env, tunnelID string) []any {
	cfg := e.cf.TunnelConfigs[tunnelID]
	rules, _ := cfg.Config["ingress"].([]any)
	return rules
}

func TestTunnelLifecycle(t *testing.T) {
	tun := tunnel("edge")
	e := newEnv(t, tun)
	r := &TunnelReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients, CloudflaredImage: "cloudflare/cloudflared:test"}

	if _, err := r.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	e.get(tun)
	assertReady(t, tun.Status.Conditions, true)
	if tun.Status.TunnelID == "" || tun.Status.TunnelName != "edge" || tun.Status.TokenSecretName != "edge-cloudflared" {
		t.Fatalf("status: %+v", tun.Status)
	}
	remote := e.cf.Tunnels[tun.Status.TunnelID]
	if remote.ConfigSrc != "cloudflare" {
		t.Fatalf("tunnel should be remotely managed: %+v", remote)
	}

	// Token secret.
	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = "edge-cloudflared", testNS
	e.get(secret)
	if string(secret.Data[cloudflared.TokenKey]) != remote.Token {
		t.Fatalf("token secret mismatch: %q", secret.Data[cloudflared.TokenKey])
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != "edge" {
		t.Fatalf("secret owner refs: %+v", secret.OwnerReferences)
	}

	// Deployment.
	dep := &appsv1.Deployment{}
	dep.Name, dep.Namespace = "edge-cloudflared", testNS
	e.get(dep)
	if *dep.Spec.Replicas != 2 || dep.Spec.Template.Spec.Containers[0].Image != "cloudflare/cloudflared:test" {
		t.Fatalf("deployment: replicas=%d image=%s", *dep.Spec.Replicas, dep.Spec.Template.Spec.Containers[0].Image)
	}
	if dep.Spec.Template.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name != "edge-cloudflared" {
		t.Fatal("deployment does not reference the token secret")
	}

	// Config has only the catch-all.
	rules := ingressRules(e, tun.Status.TunnelID)
	if len(rules) != 1 || rules[0].(map[string]any)["service"] != "http_status:404" {
		t.Fatalf("initial config: %+v", rules)
	}
	if tun.Status.IngressCount != 0 || tun.Status.ConfigVersion != 1 {
		t.Fatalf("status counts: %+v", tun.Status)
	}

	// Second reconcile does not push a new version.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	if e.cf.TunnelConfigs[tun.Status.TunnelID].Version != 1 {
		t.Fatal("config pushed again without changes")
	}

	// Scale change and disable cloudflared.
	e.get(tun)
	tun.Spec.Cloudflared.Replicas = ptr(int32(3))
	if err := e.k8s.Update(e.ctx, tun); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	e.get(dep)
	if *dep.Spec.Replicas != 3 {
		t.Fatalf("replicas not updated: %d", *dep.Spec.Replicas)
	}
	e.get(tun)
	tun.Spec.Cloudflared.Enabled = ptr(false)
	if err := e.k8s.Update(e.ctx, tun); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	if e.exists(dep) {
		t.Fatal("deployment should be removed when cloudflared is disabled")
	}

	// Delete.
	e.get(tun)
	e.delete(tun)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	if e.cf.Tunnels[tun.Status.TunnelID].DeletedAt == nil {
		t.Fatal("tunnel not deleted on Cloudflare")
	}
	if e.exists(tun) {
		t.Fatal("tunnel object should be gone")
	}
}

func TestTunnelAdoptsByName(t *testing.T) {
	tun := tunnel("edge")
	tun.Spec.Name = "shared-tunnel"
	e := newEnv(t, tun)
	e.cf.Tunnels["pre"] = fake.Tunnel{ID: "pre", Name: "shared-tunnel", Status: "healthy", ConfigSrc: "cloudflare", RemoteConfig: true, Token: "tok", Connections: []any{}}
	r := &TunnelReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	e.get(tun)
	if tun.Status.TunnelID != "pre" || tun.Status.ConnectorStatus != "healthy" {
		t.Fatalf("expected adoption: %+v", tun.Status)
	}
	if len(e.cf.Tunnels) != 1 {
		t.Fatal("a second tunnel was created")
	}
}

func TestTunnelWithIngresses(t *testing.T) {
	tun := tunnel("edge")
	noTLS := true
	app := tunnelIngress("app", "edge", "app.example.com")
	app.Spec.OriginRequest = &v1alpha1.OriginRequest{NoTLSVerify: &noTLS}
	api := tunnelIngress("api", "edge", "app.example.com")
	api.Spec.Path = "/api"
	api.Spec.Service = v1alpha1.TunnelIngressService{URL: "http://api.default.svc:9000"}
	other := tunnelIngress("other", "other-tunnel", "other.example.com")
	e := newEnv(t, tun, app, api, other)
	e.cf.AddZone("example.com")
	tr := &TunnelReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	ir := &TunnelIngressReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	dr := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}

	// Ingress reconciled before the tunnel exists on Cloudflare: not ready, no error.
	res, err := ir.Reconcile(e.ctx, e.req(testNS, "app"))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("ingress before tunnel: %v %+v", err, res)
	}
	e.get(app)
	assertReady(t, app.Status.Conditions, false)

	if _, err := tr.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	e.get(tun)
	rules := ingressRules(e, tun.Status.TunnelID)
	if len(rules) != 3 {
		t.Fatalf("expected 2 rules + catch-all, got %+v", rules)
	}
	first := rules[0].(map[string]any)
	second := rules[1].(map[string]any)
	if first["hostname"] != "app.example.com" || first["path"] != "/api" || first["service"] != "http://api.default.svc:9000" {
		t.Fatalf("path rule must come first: %+v", first)
	}
	if second["service"] != "http://web.default.svc.cluster.local:8080" {
		t.Fatalf("service url: %+v", second)
	}
	if or, _ := second["originRequest"].(map[string]any); or["noTLSVerify"] != true {
		t.Fatalf("origin request not pushed: %+v", second)
	}
	if tun.Status.IngressCount != 2 || len(tun.Status.SyncedHostnames) != 2 {
		t.Fatalf("status: %+v", tun.Status)
	}

	// Ingress creates the DNSRecord, then waits for it.
	if _, err := ir.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(app)
	assertReady(t, app.Status.Conditions, false)
	rec := &v1alpha1.DNSRecord{}
	rec.Name, rec.Namespace = "app-cname", testNS
	e.get(rec)
	if rec.Spec.Type != "CNAME" || rec.Spec.Content != tun.Status.TunnelID+".cfargotunnel.com" || !rec.Spec.Proxied || rec.Spec.Zone.ID == "" {
		t.Fatalf("dns record spec: %+v", rec.Spec)
	}
	if len(rec.OwnerReferences) != 1 || rec.OwnerReferences[0].Kind != "TunnelIngress" {
		t.Fatalf("dns record owner: %+v", rec.OwnerReferences)
	}
	if _, err := dr.Reconcile(e.ctx, e.req(testNS, "app-cname")); err != nil {
		t.Fatal(err)
	}
	if _, err := ir.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(app)
	assertReady(t, app.Status.Conditions, true)
	if app.Status.ServiceURL != "http://web.default.svc.cluster.local:8080" || app.Status.TunnelID != tun.Status.TunnelID {
		t.Fatalf("ingress status: %+v", app.Status)
	}

	// Removing an ingress drops its rule on the next tunnel reconcile.
	e.delete(api)
	if _, err := tr.Reconcile(e.ctx, e.req(testNS, "edge")); err != nil {
		t.Fatal(err)
	}
	if rules := ingressRules(e, tun.Status.TunnelID); len(rules) != 2 {
		t.Fatalf("rule not removed: %+v", rules)
	}

	// DNS disabled: no record, no requirement.
	e.get(app)
	app.Spec.DNS.Enabled = ptr(false)
	if err := e.k8s.Update(e.ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err := ir.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(app)
	assertReady(t, app.Status.Conditions, true)
	if app.Status.DNSRecordName != "" {
		t.Fatal("dnsRecordName should be cleared")
	}
}

func TestTunnelIngressInvalidService(t *testing.T) {
	ti := tunnelIngress("bad", "edge", "bad.example.com")
	ti.Spec.Service.Port = 0
	e := newEnv(t, tunnel("edge"), ti)
	ir := &TunnelIngressReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := ir.Reconcile(e.ctx, e.req(testNS, "bad")); err != nil {
		t.Fatal(err)
	}
	e.get(ti)
	if c := readyCondition(ti.Status.Conditions); c.Reason != v1alpha1.ReasonInvalidSpec {
		t.Fatalf("reason %s", c.Reason)
	}
}

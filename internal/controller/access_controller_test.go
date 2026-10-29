package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
)

func accessGroup(name string) *v1alpha1.AccessGroup {
	return &v1alpha1.AccessGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.AccessGroupSpec{
			AccountRef: v1alpha1.AccountRef{Name: testAccount},
			Include:    []v1alpha1.AccessRule{{EmailDomains: []string{"example.com"}}},
			Require:    []v1alpha1.AccessRule{{Countries: []string{"PL", "DE"}}},
		},
	}
}

func serviceToken(name string) *v1alpha1.AccessServiceToken {
	return &v1alpha1.AccessServiceToken{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec:       v1alpha1.AccessServiceTokenSpec{AccountRef: v1alpha1.AccountRef{Name: testAccount}, Duration: "720h"},
	}
}

func accessApp(name string) *v1alpha1.AccessApplication {
	return &v1alpha1.AccessApplication{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.AccessApplicationSpec{
			AccountRef:      v1alpha1.AccountRef{Name: testAccount},
			Domain:          "app.example.com",
			SessionDuration: "12h",
			Policies: []v1alpha1.AccessPolicy{
				{Name: "ci", Decision: "non_identity", Include: []v1alpha1.AccessRule{{ServiceTokens: []v1alpha1.AccessRuleRef{{Name: "ci"}}}}},
				{Name: "staff", Decision: "allow", Include: []v1alpha1.AccessRule{{Groups: []v1alpha1.AccessRuleRef{{Name: "staff"}}}}, Exclude: []v1alpha1.AccessRule{{Emails: []string{"bot@example.com"}}}},
			},
		},
	}
}

func TestAccessGroupLifecycle(t *testing.T) {
	g := accessGroup("staff")
	e := newEnv(t, g)
	r := &AccessGroupReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "staff")); err != nil {
		t.Fatal(err)
	}
	e.get(g)
	assertReady(t, g.Status.Conditions, true)
	remote := e.cf.AccessGroups[g.Status.GroupID]
	if remote["name"] != "staff" {
		t.Fatalf("remote group: %+v", remote)
	}
	if req := remote["require"].([]any); len(req) != 2 {
		t.Fatalf("expected two geo rules, got %+v", req)
	}

	// No-op on second pass.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "staff")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("PUT ") != 0 {
		t.Fatal("unexpected update")
	}

	// Update.
	e.get(g)
	g.Spec.Include = append(g.Spec.Include, v1alpha1.AccessRule{Emails: []string{"contractor@other.com"}})
	if err := e.k8s.Update(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "staff")); err != nil {
		t.Fatal(err)
	}
	if inc := e.cf.AccessGroups[g.Status.GroupID]["include"].([]any); len(inc) != 2 {
		t.Fatalf("include not updated: %+v", inc)
	}

	// Delete.
	e.get(g)
	e.delete(g)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "staff")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.AccessGroups) != 0 {
		t.Fatal("group not deleted")
	}
}

func TestAccessServiceTokenLifecycle(t *testing.T) {
	st := serviceToken("ci")
	e := newEnv(t, st)
	r := &AccessServiceTokenReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "ci")); err != nil {
		t.Fatal(err)
	}
	e.get(st)
	assertReady(t, st.Status.Conditions, true)
	if st.Status.TokenID == "" || st.Status.ClientID == "" || st.Status.ExpiresAt == nil || st.Status.SecretName != "ci-access-token" {
		t.Fatalf("status: %+v", st.Status)
	}
	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = "ci-access-token", testNS
	e.get(secret)
	remote := e.cf.ServiceTokens[st.Status.TokenID]
	if string(secret.Data["CF_ACCESS_CLIENT_ID"]) != remote.ClientID || string(secret.Data["CF_ACCESS_CLIENT_SECRET"]) != remote.ClientSecret {
		t.Fatal("secret does not hold the credentials")
	}

	// Steady state keeps the same secret.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "ci")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("POST /accounts/"+e.cf.AccountID+"/access/service_tokens/") != 0 {
		t.Fatal("unexpected rotate/refresh")
	}

	// Lost Secret: rotate.
	e.delete(secret)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "ci")); err != nil {
		t.Fatal(err)
	}
	e.get(secret)
	rotated := e.cf.ServiceTokens[st.Status.TokenID]
	if rotated.ClientSecret == remote.ClientSecret || string(secret.Data["CF_ACCESS_CLIENT_SECRET"]) != rotated.ClientSecret {
		t.Fatal("secret should be rotated and stored")
	}

	// Near expiry: refresh.
	soon := e.cf.ServiceTokens[st.Status.TokenID]
	soon.ExpiresAt = time.Now().Add(time.Hour)
	e.cf.ServiceTokens[st.Status.TokenID] = soon
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "ci")); err != nil {
		t.Fatal(err)
	}
	if e.cf.ServiceTokens[st.Status.TokenID].ExpiresAt.Before(time.Now().Add(24*time.Hour)) {
		t.Fatal("token should have been refreshed")
	}

	// Delete.
	e.get(st)
	e.delete(st)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "ci")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.ServiceTokens) != 0 {
		t.Fatal("service token not deleted")
	}
}

func TestAccessApplicationWithDependencies(t *testing.T) {
	app := accessApp("app")
	e := newEnv(t, app, accessGroup("staff"), serviceToken("ci"))
	ar := &AccessApplicationReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	gr := &AccessGroupReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	tr := &AccessServiceTokenReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}

	// Dependencies not ready yet: quiet requeue.
	res, err := ar.Reconcile(e.ctx, e.req(testNS, "app"))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("expected quiet requeue: %v %+v", err, res)
	}
	e.get(app)
	if c := readyCondition(app.Status.Conditions); c.Reason != v1alpha1.ReasonDependencyNotReady {
		t.Fatalf("reason %s: %s", c.Reason, c.Message)
	}
	if len(e.cf.AccessApps) != 0 {
		t.Fatal("app must not be created before dependencies resolve")
	}

	if _, err := gr.Reconcile(e.ctx, e.req(testNS, "staff")); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Reconcile(e.ctx, e.req(testNS, "ci")); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(app)
	assertReady(t, app.Status.Conditions, true)
	if app.Status.ApplicationID == "" || app.Status.AUD == "" || app.Status.PolicyCount != 2 {
		t.Fatalf("status: %+v", app.Status)
	}
	g := &v1alpha1.AccessGroup{}
	g.Name, g.Namespace = "staff", testNS
	e.get(g)
	st := &v1alpha1.AccessServiceToken{}
	st.Name, st.Namespace = "ci", testNS
	e.get(st)

	remote := e.cf.AccessApps[app.Status.ApplicationID]
	if remote["session_duration"] != "12h" || remote["type"] != "self_hosted" {
		t.Fatalf("remote app: %+v", remote)
	}
	policies := remote["policies"].([]any)
	first := policies[0].(map[string]any)
	second := policies[1].(map[string]any)
	if first["decision"] != "non_identity" || first["precedence"] != float64(1) {
		t.Fatalf("first policy: %+v", first)
	}
	tokRule := first["include"].([]any)[0].(map[string]any)["service_token"].(map[string]any)
	if tokRule["token_id"] != st.Status.TokenID {
		t.Fatalf("service token not resolved: %+v", tokRule)
	}
	groupRule := second["include"].([]any)[0].(map[string]any)["group"].(map[string]any)
	if groupRule["id"] != g.Status.GroupID {
		t.Fatalf("group not resolved: %+v", groupRule)
	}
	if excl := second["exclude"].([]any); len(excl) != 1 {
		t.Fatalf("exclude: %+v", excl)
	}

	// No-op on steady state.
	puts := e.cf.CallCount("PUT /accounts/" + e.cf.AccountID + "/access/apps/")
	if _, err := ar.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("PUT /accounts/"+e.cf.AccountID+"/access/apps/") != puts {
		t.Fatal("unexpected update in steady state")
	}

	// Policy edit updates in place.
	e.get(app)
	app.Spec.Policies = app.Spec.Policies[1:]
	app.Spec.AppLauncherVisible = ptr(true)
	if err := e.k8s.Update(e.ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err := ar.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(app)
	if app.Status.PolicyCount != 1 || e.cf.AccessApps[app.Status.ApplicationID]["app_launcher_visible"] != true {
		t.Fatalf("update not applied: %+v", e.cf.AccessApps[app.Status.ApplicationID])
	}

	// Delete.
	e.get(app)
	e.delete(app)
	if _, err := ar.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.AccessApps) != 0 {
		t.Fatal("app not deleted")
	}
}

func TestAccessApplicationAdoptsByDomain(t *testing.T) {
	app := accessApp("app")
	app.Spec.Policies = []v1alpha1.AccessPolicy{{Name: "all", Decision: "allow", Include: []v1alpha1.AccessRule{{Everyone: true}}}}
	e := newEnv(t, app)
	e.cf.AccessApps["pre"] = map[string]any{"id": "pre", "aud": "aud-pre", "name": "old", "domain": "app.example.com", "type": "self_hosted", "policies": []any{}}
	ar := &AccessApplicationReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := ar.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(app)
	if app.Status.ApplicationID != "pre" || app.Status.AUD != "aud-pre" {
		t.Fatalf("expected adoption: %+v", app.Status)
	}
	if e.cf.AccessApps["pre"]["name"] != "app" || len(e.cf.AccessApps) != 1 {
		t.Fatalf("adopted app not updated: %+v", e.cf.AccessApps)
	}
}

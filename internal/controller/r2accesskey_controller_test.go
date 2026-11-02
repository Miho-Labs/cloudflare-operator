package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

func r2AccessKey(name string) *v1alpha1.R2AccessKey {
	return &v1alpha1.R2AccessKey{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.R2AccessKeySpec{
			AccountRef: v1alpha1.AccountRef{Name: testAccount},
			Permission: v1alpha1.R2PermissionReadOnly,
			Buckets: []v1alpha1.R2BucketTarget{
				{Ref: &v1alpha1.LocalObjectRef{Name: "assets"}},
				{Name: "legacy-data", Jurisdiction: "eu"},
			},
		},
	}
}

func tokenResources(e *env, tokenID string) map[string]any {
	policy := e.cf.APITokens[tokenID].Policies[0].(map[string]any)
	return policy["resources"].(map[string]any)
}

func TestR2AccessKeyLifecycle(t *testing.T) {
	bucket := r2bucket("assets")
	bucket.Spec.APIToken.Enabled = false
	key := r2AccessKey("reader")
	e := newEnv(t, bucket, key)
	br := &R2BucketReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	kr := &R2AccessKeyReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}

	// Bucket not created yet: quiet requeue.
	res, err := kr.Reconcile(e.ctx, e.req(testNS, "reader"))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("expected quiet requeue: %v %+v", err, res)
	}
	e.get(key)
	if c := readyCondition(key.Status.Conditions); c.Reason != v1alpha1.ReasonDependencyNotReady {
		t.Fatalf("reason %s: %s", c.Reason, c.Message)
	}
	if len(e.cf.APITokens) != 0 {
		t.Fatal("no token should exist before the bucket is ready")
	}

	if _, err := br.Reconcile(e.ctx, e.req(testNS, "assets")); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Reconcile(e.ctx, e.req(testNS, "reader")); err != nil {
		t.Fatal(err)
	}
	e.get(key)
	assertReady(t, key.Status.Conditions, true)
	if key.Status.TokenID == "" || key.Status.SecretName != "reader-r2-credentials" || len(key.Status.Buckets) != 2 {
		t.Fatalf("status: %+v", key.Status)
	}
	resources := tokenResources(e, key.Status.TokenID)
	for _, want := range []string{
		"com.cloudflare.edge.r2.bucket." + e.cf.AccountID + "_default_assets",
		"com.cloudflare.edge.r2.bucket." + e.cf.AccountID + "_eu_legacy-data",
	} {
		if _, ok := resources[want]; !ok {
			t.Fatalf("missing resource %s in %+v", want, resources)
		}
	}
	if groups := e.cf.APITokens[key.Status.TokenID].Policies[0].(map[string]any)["permission_groups"].([]any); len(groups) != 1 {
		t.Fatalf("read-only should have one permission group: %+v", groups)
	}
	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = "reader-r2-credentials", testNS
	e.get(secret)
	tok := e.cf.APITokens[key.Status.TokenID]
	if string(secret.Data["AWS_ACCESS_KEY_ID"]) != tok.ID || string(secret.Data["AWS_SECRET_ACCESS_KEY"]) != cloudflare.R2SecretAccessKey(tok.Value) {
		t.Fatal("credentials mismatch")
	}
	if string(secret.Data["BUCKET_NAMES"]) != "assets,legacy-data" || secret.Data["BUCKET_NAME"] != nil {
		t.Fatalf("bucket data: %v", secret.Data)
	}
	if string(secret.Data["AWS_ENDPOINT_URL"]) != "https://"+e.cf.AccountID+".r2.cloudflarestorage.com" {
		t.Fatalf("endpoint: %s", secret.Data["AWS_ENDPOINT_URL"])
	}

	// Steady state.
	if _, err := kr.Reconcile(e.ctx, e.req(testNS, "reader")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.APITokens) != 1 {
		t.Fatal("token recreated without reason")
	}

	// Removing a bucket from the scope rotates the token.
	prev := key.Status.TokenID
	e.get(key)
	key.Spec.Buckets = key.Spec.Buckets[:1]
	if err := e.k8s.Update(e.ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Reconcile(e.ctx, e.req(testNS, "reader")); err != nil {
		t.Fatal(err)
	}
	e.get(key)
	if key.Status.TokenID == prev || len(e.cf.APITokens) != 1 {
		t.Fatalf("token should be rotated: %s -> %s (%d tokens)", prev, key.Status.TokenID, len(e.cf.APITokens))
	}
	if n := len(tokenResources(e, key.Status.TokenID)); n != 1 {
		t.Fatalf("expected one resource, got %d", n)
	}
	e.get(secret)
	if string(secret.Data["BUCKET_NAME"]) != "assets" || string(secret.Data["AWS_ACCESS_KEY_ID"]) != key.Status.TokenID {
		t.Fatalf("secret not refreshed: %v", secret.Data)
	}

	// Delete removes the token and keeps the bucket untouched.
	e.delete(key)
	if _, err := kr.Reconcile(e.ctx, e.req(testNS, "reader")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.APITokens) != 0 || e.exists(key) {
		t.Fatal("token or object still present after delete")
	}
	if _, ok := e.cf.Buckets["assets"]; !ok {
		t.Fatal("bucket must not be touched by the key")
	}
}

func TestR2AccessKeyAdmin(t *testing.T) {
	key := r2AccessKey("admin")
	key.Spec.Permission = v1alpha1.R2PermissionAdmin
	key.Spec.Buckets = nil
	e := newEnv(t, key)
	kr := &R2AccessKeyReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := kr.Reconcile(e.ctx, e.req(testNS, "admin")); err != nil {
		t.Fatal(err)
	}
	e.get(key)
	assertReady(t, key.Status.Conditions, true)
	resources := tokenResources(e, key.Status.TokenID)
	if _, ok := resources["com.cloudflare.api.account."+e.cf.AccountID]; !ok || len(resources) != 1 {
		t.Fatalf("admin token should be scoped to the account: %+v", resources)
	}
}

func TestR2AccessKeyNoBuckets(t *testing.T) {
	key := r2AccessKey("empty")
	key.Spec.Buckets = nil
	e := newEnv(t, key)
	kr := &R2AccessKeyReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := kr.Reconcile(e.ctx, e.req(testNS, "empty")); err != nil {
		t.Fatal(err)
	}
	e.get(key)
	if c := readyCondition(key.Status.Conditions); c.Reason != v1alpha1.ReasonInvalidSpec {
		t.Fatalf("reason %s", c.Reason)
	}
}

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

func r2bucket(name string) *v1alpha1.R2Bucket {
	return &v1alpha1.R2Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.R2BucketSpec{
			AccountRef:   v1alpha1.AccountRef{Name: testAccount},
			LocationHint: "weur",
			APIToken:     v1alpha1.R2APITokenSpec{Enabled: true, Permission: v1alpha1.R2PermissionReadWrite},
		},
	}
}

func TestR2BucketLifecycle(t *testing.T) {
	b := r2bucket("assets")
	e := newEnv(t, b)
	r := &R2BucketReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "assets")); err != nil {
		t.Fatal(err)
	}
	e.get(b)
	assertReady(t, b.Status.Conditions, true)
	if b.Status.BucketName != "assets" || b.Status.TokenID == "" || b.Status.CredentialsSecretName != "assets-r2-credentials" {
		t.Fatalf("status: %+v", b.Status)
	}
	if b.Status.Endpoint != "https://"+e.cf.AccountID+".r2.cloudflarestorage.com" {
		t.Fatalf("endpoint: %s", b.Status.Endpoint)
	}
	if _, ok := e.cf.Buckets["assets"]; !ok {
		t.Fatal("bucket not created")
	}
	tok, ok := e.cf.APITokens[b.Status.TokenID]
	if !ok {
		t.Fatal("token not created")
	}
	policy := tok.Policies[0].(map[string]any)
	resources := policy["resources"].(map[string]any)
	if _, ok := resources["com.cloudflare.edge.r2.bucket."+e.cf.AccountID+"_default_assets"]; !ok {
		t.Fatalf("token not scoped to bucket: %+v", resources)
	}
	if groups := policy["permission_groups"].([]any); len(groups) != 2 {
		t.Fatalf("expected read+write permission groups, got %+v", groups)
	}

	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = "assets-r2-credentials", testNS
	e.get(secret)
	if string(secret.Data["AWS_ACCESS_KEY_ID"]) != b.Status.TokenID {
		t.Fatal("access key id must be the token id")
	}
	if string(secret.Data["AWS_SECRET_ACCESS_KEY"]) != cloudflare.R2SecretAccessKey(tok.Value) {
		t.Fatal("secret access key must be sha256(token value)")
	}
	if string(secret.Data["AWS_ENDPOINT_URL"]) != b.Status.Endpoint || string(secret.Data["BUCKET_NAME"]) != "assets" || string(secret.Data["AWS_REGION"]) != "auto" {
		t.Fatalf("secret data: %v", secret.Data)
	}

	// Steady state: no new token.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "assets")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.APITokens) != 1 {
		t.Fatal("token recreated without reason")
	}

	// Secret deleted: token is rotated and the Secret recreated.
	e.delete(secret)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "assets")); err != nil {
		t.Fatal(err)
	}
	e.get(b)
	if _, stillThere := e.cf.APITokens[tok.ID]; stillThere || b.Status.TokenID == tok.ID {
		t.Fatal("old token should be replaced")
	}
	e.get(secret)
	if string(secret.Data["AWS_ACCESS_KEY_ID"]) != b.Status.TokenID {
		t.Fatal("secret not refreshed with new token")
	}

	// Permission change rotates the token too.
	e.get(b)
	b.Spec.APIToken.Permission = v1alpha1.R2PermissionReadOnly
	if err := e.k8s.Update(e.ctx, b); err != nil {
		t.Fatal(err)
	}
	prev := b.Status.TokenID
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "assets")); err != nil {
		t.Fatal(err)
	}
	e.get(b)
	if b.Status.TokenID == prev {
		t.Fatal("token should be rotated on permission change")
	}
	if groups := e.cf.APITokens[b.Status.TokenID].Policies[0].(map[string]any)["permission_groups"].([]any); len(groups) != 1 {
		t.Fatalf("read-only token should have one permission group: %+v", groups)
	}

	// Non-empty bucket blocks deletion, then succeeds once emptied.
	e.cf.NonEmptyBucket["assets"] = true
	e.get(b)
	e.delete(b)
	res, err := r.Reconcile(e.ctx, e.req(testNS, "assets"))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("non-empty bucket should requeue: %v %+v", err, res)
	}
	if len(e.cf.APITokens) != 0 {
		t.Fatal("token should be deleted even when the bucket is kept")
	}
	if !e.exists(b) {
		t.Fatal("object must stay until the bucket is gone")
	}
	e.cf.NonEmptyBucket["assets"] = false
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "assets")); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.cf.Buckets["assets"]; ok {
		t.Fatal("bucket not deleted")
	}
	if e.exists(b) {
		t.Fatal("object should be gone")
	}
}

func TestR2BucketNoTokenAndRetain(t *testing.T) {
	b := r2bucket("logs")
	b.Spec.Name = "company-logs"
	b.Spec.APIToken.Enabled = false
	b.Spec.DeletionPolicy = v1alpha1.DeletionPolicyRetain
	e := newEnv(t, b)
	r := &R2BucketReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "logs")); err != nil {
		t.Fatal(err)
	}
	e.get(b)
	if b.Status.BucketName != "company-logs" || b.Status.TokenID != "" || len(e.cf.APITokens) != 0 {
		t.Fatalf("status: %+v tokens=%d", b.Status, len(e.cf.APITokens))
	}
	e.delete(b)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "logs")); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.cf.Buckets["company-logs"]; !ok {
		t.Fatal("bucket should be retained")
	}
}

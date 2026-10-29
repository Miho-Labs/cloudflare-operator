package controller

import (
	"testing"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestCloudflareAccountVerifies(t *testing.T) {
	e := newEnv(t)
	r := &CloudflareAccountReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req("", testAccount)); err != nil {
		t.Fatal(err)
	}
	acc := &v1alpha1.CloudflareAccount{}
	acc.Name = testAccount
	e.get(acc)
	assertReady(t, acc.Status.Conditions, true)
	if acc.Status.AccountName != e.cf.AccountName || acc.Status.TokenStatus != "active" || acc.Status.LastVerified == nil {
		t.Fatalf("status not filled: %+v", acc.Status)
	}
}

func TestCloudflareAccountBadToken(t *testing.T) {
	e := newEnv(t)
	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = "cloudflare-token", testNS
	e.get(secret)
	secret.Data["apiToken"] = []byte("wrong")
	if err := e.k8s.Update(e.ctx, secret); err != nil {
		t.Fatal(err)
	}
	r := &CloudflareAccountReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	res, err := r.Reconcile(e.ctx, e.req("", testAccount))
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue")
	}
	acc := &v1alpha1.CloudflareAccount{}
	acc.Name = testAccount
	e.get(acc)
	assertReady(t, acc.Status.Conditions, false)

	// Dependents see the account as not ready.
	if _, _, err := e.clients.Resolve(e.ctx, v1alpha1.AccountRef{Name: testAccount}); err == nil {
		t.Fatal("expected not-ready error")
	}
}

func TestCloudflareAccountMissingSecret(t *testing.T) {
	e := newEnv(t)
	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = "cloudflare-token", testNS
	e.delete(secret)
	r := &CloudflareAccountReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req("", testAccount)); err != nil {
		t.Fatal(err)
	}
	acc := &v1alpha1.CloudflareAccount{}
	acc.Name = testAccount
	e.get(acc)
	assertReady(t, acc.Status.Conditions, false)
	if c := readyCondition(acc.Status.Conditions); c.Reason != v1alpha1.ReasonInvalidSpec {
		t.Fatalf("reason %s", c.Reason)
	}
}

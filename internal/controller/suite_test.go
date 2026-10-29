package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	cffake "github.com/miho-labs/cloudflare-operator/internal/cloudflare/fake"
)

const (
	testNS      = "default"
	testAccount = "main"
)

type env struct {
	t        *testing.T
	ctx      context.Context
	cf       *cffake.Server
	k8s      client.Client
	clients  *ClientFactory
	recorder record.EventRecorder
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// newEnv starts a fake Cloudflare API and a fake Kubernetes client pre-loaded with a verified
// CloudflareAccount named "main" plus the given objects.
func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	srv := cffake.New()
	t.Cleanup(srv.Close)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-token", Namespace: testNS},
		Data:       map[string][]byte{"apiToken": []byte(srv.Token)},
	}
	account := &v1alpha1.CloudflareAccount{
		ObjectMeta: metav1.ObjectMeta{Name: testAccount},
		Spec: v1alpha1.CloudflareAccountSpec{
			AccountID:         srv.AccountID,
			APITokenSecretRef: v1alpha1.SecretKeyRef{Name: "cloudflare-token", Namespace: testNS, Key: "apiToken"},
		},
	}
	all := append([]client.Object{secret, account}, objs...)

	k8s := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(all...).
		WithStatusSubresource(
			&v1alpha1.CloudflareAccount{}, &v1alpha1.DNSRecord{}, &v1alpha1.Tunnel{}, &v1alpha1.TunnelIngress{},
			&v1alpha1.R2Bucket{}, &v1alpha1.AccessApplication{}, &v1alpha1.AccessGroup{}, &v1alpha1.AccessServiceToken{},
		).
		WithIndex(&v1alpha1.DNSRecord{}, IndexAccountRef, AccountRefIndexer).
		WithIndex(&v1alpha1.Tunnel{}, IndexAccountRef, AccountRefIndexer).
		WithIndex(&v1alpha1.R2Bucket{}, IndexAccountRef, AccountRefIndexer).
		WithIndex(&v1alpha1.AccessApplication{}, IndexAccountRef, AccountRefIndexer).
		WithIndex(&v1alpha1.AccessGroup{}, IndexAccountRef, AccountRefIndexer).
		WithIndex(&v1alpha1.AccessServiceToken{}, IndexAccountRef, AccountRefIndexer).
		WithIndex(&v1alpha1.TunnelIngress{}, IndexTunnelRef, TunnelRefIndexer).
		WithIndex(&v1alpha1.CloudflareAccount{}, IndexSecretRef, SecretRefIndexer).
		Build()

	factory := NewClientFactory(k8s)
	factory.BaseURL = srv.URL
	return &env{t: t, ctx: context.Background(), cf: srv, k8s: k8s, clients: factory, recorder: record.NewFakeRecorder(100)}
}

func (e *env) req(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

// get refreshes obj from the fake API server.
func (e *env) get(obj client.Object) {
	e.t.Helper()
	if err := e.k8s.Get(e.ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		e.t.Fatalf("get %T %s: %v", obj, obj.GetName(), err)
	}
}

func (e *env) delete(obj client.Object) {
	e.t.Helper()
	if err := e.k8s.Delete(e.ctx, obj); err != nil {
		e.t.Fatalf("delete %T %s: %v", obj, obj.GetName(), err)
	}
}

func (e *env) exists(obj client.Object) bool {
	e.t.Helper()
	err := e.k8s.Get(e.ctx, client.ObjectKeyFromObject(obj), obj)
	return err == nil
}

func readyCondition(conds []metav1.Condition) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == v1alpha1.ConditionReady {
			return &conds[i]
		}
	}
	return nil
}

func assertReady(t *testing.T, conds []metav1.Condition, want bool) {
	t.Helper()
	c := readyCondition(conds)
	if c == nil {
		t.Fatalf("no Ready condition")
	}
	got := c.Status == metav1.ConditionTrue
	if got != want {
		t.Fatalf("Ready=%v (%s: %s), want %v", got, c.Reason, c.Message, want)
	}
}

func ptr[T any](v T) *T { return &v }

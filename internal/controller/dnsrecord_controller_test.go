package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare/fake"
)

func dnsRecord(name string) *v1alpha1.DNSRecord {
	return &v1alpha1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1alpha1.DNSRecordSpec{
			AccountRef: v1alpha1.AccountRef{Name: testAccount},
			Zone:       v1alpha1.ZoneRef{Name: "example.com"},
			Type:       "A",
			Name:       "www.example.com",
			Content:    "192.0.2.1",
			TTL:        300,
		},
	}
}

func TestDNSRecordLifecycle(t *testing.T) {
	rec := dnsRecord("www")
	e := newEnv(t, rec)
	zoneID := e.cf.AddZone("example.com")
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}

	// First pass adds the finalizer and creates the record.
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	e.get(rec)
	assertReady(t, rec.Status.Conditions, true)
	if rec.Status.ZoneID != zoneID || rec.Status.RecordID == "" || rec.Status.FQDN != "www.example.com" {
		t.Fatalf("status: %+v", rec.Status)
	}
	if len(rec.Finalizers) != 1 {
		t.Fatalf("finalizer missing: %v", rec.Finalizers)
	}
	if got := e.cf.DNSRecords[rec.Status.RecordID]; got.Content != "192.0.2.1" || got.TTL != 300 {
		t.Fatalf("remote record: %+v", got)
	}

	// Second pass is a no-op.
	before := e.cf.CallCount("PUT ")
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	if e.cf.CallCount("PUT ") != before {
		t.Fatal("unexpected update on unchanged spec")
	}

	// Spec change updates in place.
	e.get(rec)
	rec.Spec.Content = "192.0.2.2"
	rec.Spec.Proxied = true
	if err := e.k8s.Update(e.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	if got := e.cf.DNSRecords[rec.Status.RecordID]; got.Content != "192.0.2.2" || !got.Proxied || got.TTL != 1 {
		t.Fatalf("remote record after update: %+v", got)
	}

	// Drift on Cloudflare is corrected.
	drifted := e.cf.DNSRecords[rec.Status.RecordID]
	drifted.Content = "198.51.100.9"
	e.cf.DNSRecords[rec.Status.RecordID] = drifted
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	if got := e.cf.DNSRecords[rec.Status.RecordID]; got.Content != "192.0.2.2" {
		t.Fatalf("drift not corrected: %+v", got)
	}

	// Delete removes it from Cloudflare and drops the finalizer.
	e.get(rec)
	e.delete(rec)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.DNSRecords) != 0 {
		t.Fatal("record not deleted from Cloudflare")
	}
	if e.exists(rec) {
		t.Fatal("object should be gone after finalizer removal")
	}
}

func TestDNSRecordAdoptsExisting(t *testing.T) {
	rec := dnsRecord("www")
	e := newEnv(t, rec)
	zoneID := e.cf.AddZone("example.com")
	e.cf.DNSRecords["pre"] = fake.DNSRecord{ID: "pre", ZoneID: zoneID, Type: "A", Name: "www.example.com", Content: "10.0.0.1", TTL: 1}
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	e.get(rec)
	if rec.Status.RecordID != "pre" {
		t.Fatalf("expected adoption, got %+v", rec.Status)
	}
	if got := e.cf.DNSRecords["pre"]; got.Content != "192.0.2.1" {
		t.Fatalf("adopted record not updated: %+v", got)
	}
	if len(e.cf.DNSRecords) != 1 {
		t.Fatal("duplicate record created")
	}
}

func TestDNSRecordRetain(t *testing.T) {
	rec := dnsRecord("www")
	rec.Spec.DeletionPolicy = v1alpha1.DeletionPolicyRetain
	e := newEnv(t, rec)
	e.cf.AddZone("example.com")
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	e.get(rec)
	e.delete(rec)
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	if len(e.cf.DNSRecords) != 1 {
		t.Fatal("record should have been retained")
	}
	if e.exists(rec) {
		t.Fatal("object should be gone")
	}
}

func TestDNSRecordUnknownZoneAndAccount(t *testing.T) {
	rec := dnsRecord("www")
	e := newEnv(t, rec)
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err == nil {
		t.Fatal("expected error for unknown zone")
	}
	e.get(rec)
	assertReady(t, rec.Status.Conditions, false)

	e.get(rec)
	rec.Spec.AccountRef.Name = "missing"
	if err := e.k8s.Update(e.ctx, rec); err != nil {
		t.Fatal(err)
	}
	res, err := r.Reconcile(e.ctx, e.req(testNS, "www"))
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("missing account should requeue quietly: %v %+v", err, res)
	}
	e.get(rec)
	if c := readyCondition(rec.Status.Conditions); c.Reason != v1alpha1.ReasonAccountNotFound {
		t.Fatalf("reason %s", c.Reason)
	}
}

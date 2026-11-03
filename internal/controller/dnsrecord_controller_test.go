package controller

import (
	"strings"
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

func TestDNSRecordConvertsConflictingType(t *testing.T) {
	rec := dnsRecord("app")
	rec.Spec.Type = "CNAME"
	rec.Spec.Name = "app.example.com"
	rec.Spec.Content = "tunnel.cfargotunnel.com"
	rec.Spec.Proxied = true
	e := newEnv(t, rec)
	zoneID := e.cf.AddZone("example.com")
	e.cf.DNSRecords["old-a"] = fake.DNSRecord{ID: "old-a", ZoneID: zoneID, Type: "A", Name: "app.example.com", Content: "192.0.2.1", TTL: 300}
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "app")); err != nil {
		t.Fatal(err)
	}
	e.get(rec)
	assertReady(t, rec.Status.Conditions, true)
	if rec.Status.RecordID != "old-a" {
		t.Fatalf("expected the A record to be adopted, got %+v", rec.Status)
	}
	got := e.cf.DNSRecords["old-a"]
	if got.Type != "CNAME" || got.Content != "tunnel.cfargotunnel.com" || !got.Proxied || len(e.cf.DNSRecords) != 1 {
		t.Fatalf("record not converted in place: %+v (%d records)", got, len(e.cf.DNSRecords))
	}
}

func TestDNSRecordZoneInOtherAccount(t *testing.T) {
	rec := dnsRecord("www")
	e := newEnv(t, rec)
	e.cf.Zones["foreign"] = fake.Zone{ID: "foreign", Name: "example.com", AccountID: "ffffffffffffffffffffffffffffffff"}
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err == nil {
		t.Fatal("expected error")
	}
	e.get(rec)
	c := readyCondition(rec.Status.Conditions)
	if c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, "belongs to account ffffffffffffffffffffffffffffffff") {
		t.Fatalf("condition: %+v", c)
	}
}

func TestDNSRecordMultiValueAdoptionAndRecovery(t *testing.T) {
	spf := dnsRecord("spf")
	spf.Spec.Type, spf.Spec.Name, spf.Spec.Content = "TXT", "example.com", "v=spf1 -all"
	gsv := dnsRecord("gsv")
	gsv.Spec.Type, gsv.Spec.Name, gsv.Spec.Content = "TXT", "example.com", "google-site-verification=abc"
	fresh := dnsRecord("fresh")
	fresh.Spec.Type, fresh.Spec.Name, fresh.Spec.Content = "TXT", "example.com", "brand-new"
	e := newEnv(t, spf, gsv, fresh)
	zoneID := e.cf.AddZone("example.com")
	e.cf.DNSRecords["spf"] = fake.DNSRecord{ID: "spf", ZoneID: zoneID, Type: "TXT", Name: "example.com", Content: "v=spf1 -all", TTL: 1}
	e.cf.DNSRecords["gsv"] = fake.DNSRecord{ID: "gsv", ZoneID: zoneID, Type: "TXT", Name: "example.com", Content: "google-site-verification=abc", TTL: 1}
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}

	for _, name := range []string{"spf", "gsv", "fresh"} {
		if _, err := r.Reconcile(e.ctx, e.req(testNS, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	e.get(spf)
	e.get(gsv)
	e.get(fresh)
	if spf.Status.RecordID != "spf" || gsv.Status.RecordID != "gsv" {
		t.Fatalf("adoption must match content: spf=%s gsv=%s", spf.Status.RecordID, gsv.Status.RecordID)
	}
	if fresh.Status.RecordID == "" || fresh.Status.RecordID == "spf" || fresh.Status.RecordID == "gsv" {
		t.Fatalf("unmatched content must create a new record, got %q", fresh.Status.RecordID)
	}
	if len(e.cf.DNSRecords) != 3 {
		t.Fatalf("expected 3 remote records, got %d", len(e.cf.DNSRecords))
	}
	assertReady(t, fresh.Status.Conditions, true)

	// Recovery: an object left pointing at a sibling (old adoption bug) re-points instead of failing.
	e.get(gsv)
	gsv.Status.RecordID = "spf"
	if err := e.k8s.Status().Update(e.ctx, gsv); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "gsv")); err != nil {
		t.Fatal(err)
	}
	e.get(gsv)
	assertReady(t, gsv.Status.Conditions, true)
	if gsv.Status.RecordID != "gsv" {
		t.Fatalf("expected re-point to gsv, got %s", gsv.Status.RecordID)
	}
	if e.cf.DNSRecords["spf"].Content != "v=spf1 -all" {
		t.Fatal("the sibling record must not be modified")
	}
}

func TestDNSRecordCommentTruncatedAndAuthQuiet(t *testing.T) {
	rec := dnsRecord("www")
	rec.Spec.Comment = strings.Repeat("c", 150)
	e := newEnv(t, rec)
	zoneID := e.cf.AddZone("example.com")
	r := &DNSRecordReconciler{Client: e.k8s, Recorder: e.recorder, Clients: e.clients}
	if _, err := r.Reconcile(e.ctx, e.req(testNS, "www")); err != nil {
		t.Fatal(err)
	}
	e.get(rec)
	assertReady(t, rec.Status.Conditions, true)
	if got := e.cf.DNSRecords[rec.Status.RecordID].Comment; len(got) != 100 {
		t.Fatalf("comment length %d", len(got))
	}

	// A 403 sets a hint and requeues quietly instead of returning an error.
	e.cf.Fail["GET /zones/"+zoneID+"/dns_records/"+rec.Status.RecordID] = 403
	res, err := r.Reconcile(e.ctx, e.req(testNS, "www"))
	if err != nil || res.RequeueAfter != requeueAuth {
		t.Fatalf("auth error should requeue quietly: %v %+v", err, res)
	}
	e.get(rec)
	c := readyCondition(rec.Status.Conditions)
	if c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, "lacks permission") {
		t.Fatalf("condition: %+v", c)
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

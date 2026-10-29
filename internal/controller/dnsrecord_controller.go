package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// DNSRecordReconciler manages DNS records.
type DNSRecordReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=dnsrecords,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=dnsrecords/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=dnsrecords/finalizers,verbs=update

// Reconcile creates, adopts, updates or deletes the record on Cloudflare.
func (r *DNSRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rec := &v1alpha1.DNSRecord{}
	if err := r.Get(ctx, req.NamespacedName, rec); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !rec.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, rec)
	}
	if !controllerutil.ContainsFinalizer(rec, v1alpha1.Finalizer) {
		orig := rec.DeepCopy()
		controllerutil.AddFinalizer(rec, v1alpha1.Finalizer)
		if err := r.Patch(ctx, rec, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := rec.DeepCopy()
	res, err := finish(rec, &rec.Status, r.reconcile(ctx, rec), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, rec); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *DNSRecordReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

func (r *DNSRecordReconciler) resolveZone(ctx context.Context, cl *cloudflare.Client, rec *v1alpha1.DNSRecord) (*cloudflare.ZoneInfo, error) {
	if rec.Status.ZoneID != "" {
		return &cloudflare.ZoneInfo{ID: rec.Status.ZoneID}, nil
	}
	if rec.Spec.Zone.ID != "" {
		return cl.ZoneByID(ctx, rec.Spec.Zone.ID)
	}
	return cl.ZoneByName(ctx, rec.Spec.Zone.Name)
}

func desiredRecord(rec *v1alpha1.DNSRecord, zoneID string) cloudflare.DNSRecord {
	ttl := rec.Spec.TTL
	if ttl == 0 || rec.Spec.Proxied {
		ttl = 1
	}
	return cloudflare.DNSRecord{
		ID:       rec.Status.RecordID,
		ZoneID:   zoneID,
		Type:     strings.ToUpper(rec.Spec.Type),
		Name:     rec.Spec.Name,
		Content:  rec.Spec.Content,
		TTL:      ttl,
		Proxied:  rec.Spec.Proxied,
		Priority: rec.Spec.Priority,
		Comment:  rec.Spec.Comment,
		Tags:     rec.Spec.Tags,
	}
}

func (r *DNSRecordReconciler) reconcile(ctx context.Context, rec *v1alpha1.DNSRecord) error {
	cl, _, err := r.Clients.Resolve(ctx, rec.Spec.AccountRef)
	if err != nil {
		return err
	}
	zone, err := r.resolveZone(ctx, cl, rec)
	if err != nil {
		return err
	}
	rec.Status.ZoneID = zone.ID
	desired := desiredRecord(rec, zone.ID)

	var current *cloudflare.DNSRecord
	if rec.Status.RecordID != "" {
		current, err = cl.GetDNSRecord(ctx, zone.ID, rec.Status.RecordID)
		if err != nil && !cloudflare.IsNotFound(err) {
			return err
		}
		if cloudflare.IsNotFound(err) {
			r.Recorder.Eventf(rec, corev1.EventTypeWarning, EventFailed, "record %s disappeared from Cloudflare, recreating", rec.Status.RecordID)
			rec.Status.RecordID = ""
			desired.ID = ""
		}
	}
	if current == nil {
		current, err = cl.FindDNSRecord(ctx, zone.ID, desired.Type, desired.Name)
		if err != nil {
			return err
		}
		if current != nil {
			r.Recorder.Eventf(rec, corev1.EventTypeNormal, EventAdopted, "adopted existing %s record %s (%s)", current.Type, current.Name, current.ID)
		}
	}

	switch {
	case current == nil:
		created, err := cl.CreateDNSRecord(ctx, desired)
		if err != nil {
			return err
		}
		current = created
		r.Recorder.Eventf(rec, corev1.EventTypeNormal, EventCreated, "created %s record %s", created.Type, created.Name)
	case !current.Equal(desired):
		desired.ID = current.ID
		updated, err := cl.UpdateDNSRecord(ctx, desired)
		if err != nil {
			return err
		}
		current = updated
		r.Recorder.Eventf(rec, corev1.EventTypeNormal, EventUpdated, "updated %s record %s", updated.Type, updated.Name)
	}
	rec.Status.RecordID = current.ID
	rec.Status.FQDN = current.Name
	setReady(rec, &rec.Status, v1alpha1.ReasonSynced, fmt.Sprintf("%s record %s is in sync", current.Type, current.Name))
	return nil
}

func (r *DNSRecordReconciler) finalize(ctx context.Context, rec *v1alpha1.DNSRecord) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(rec, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if deletionPolicy(rec.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && rec.Status.RecordID != "" && rec.Status.ZoneID != "" {
		cl, _, err := r.Clients.Resolve(ctx, rec.Spec.AccountRef)
		if err != nil {
			return r.finalizeError(ctx, rec, err)
		}
		if err := cl.DeleteDNSRecord(ctx, rec.Status.ZoneID, rec.Status.RecordID); err != nil {
			return r.finalizeError(ctx, rec, err)
		}
		r.Recorder.Eventf(rec, corev1.EventTypeNormal, EventDeleted, "deleted record %s from Cloudflare", rec.Status.RecordID)
	} else if rec.Status.RecordID != "" {
		r.Recorder.Eventf(rec, corev1.EventTypeNormal, EventRetained, "kept record %s on Cloudflare (deletionPolicy=Retain)", rec.Status.RecordID)
	}
	orig := rec.DeepCopy()
	controllerutil.RemoveFinalizer(rec, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, rec, client.MergeFrom(orig)))
}

func (r *DNSRecordReconciler) finalizeError(ctx context.Context, rec *v1alpha1.DNSRecord, err error) (ctrl.Result, error) {
	orig := rec.DeepCopy()
	res, rerr := finish(rec, &rec.Status, err, r.resync())
	setNotReady(rec, &rec.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, rec)
	return res, rerr
}

// SetupWithManager registers the controller.
func (r *DNSRecordReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DNSRecord{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.DNSRecordList{}, o.GetName())
		})).
		Named("dnsrecord").
		Complete(r)
}

// AccountRefIndexer extracts spec.accountRef.name for the field index of any resource type.
func AccountRefIndexer(obj client.Object) []string {
	switch o := obj.(type) {
	case *v1alpha1.DNSRecord:
		return []string{o.Spec.AccountRef.Name}
	case *v1alpha1.Tunnel:
		return []string{o.Spec.AccountRef.Name}
	case *v1alpha1.R2Bucket:
		return []string{o.Spec.AccountRef.Name}
	case *v1alpha1.AccessApplication:
		return []string{o.Spec.AccountRef.Name}
	case *v1alpha1.AccessGroup:
		return []string{o.Spec.AccountRef.Name}
	case *v1alpha1.AccessServiceToken:
		return []string{o.Spec.AccountRef.Name}
	}
	return nil
}

package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
)

// TunnelIngressReconciler validates ingress rules, creates their DNS records and reports readiness.
// The rules themselves are pushed by the Tunnel controller, which watches TunnelIngress objects.
type TunnelIngressReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnelingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnelingresses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnels,verbs=get;list;watch

// Reconcile ensures the DNS record exists and reports whether the Tunnel has picked up the rule.
func (r *TunnelIngressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ti := &v1alpha1.TunnelIngress{}
	if err := r.Get(ctx, req.NamespacedName, ti); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !ti.DeletionTimestamp.IsZero() {
		// The DNSRecord is garbage collected via its owner reference, and the Tunnel controller
		// drops the rule when it sees the deletion. Nothing to do here.
		return ctrl.Result{}, nil
	}
	orig := ti.DeepCopy()
	res, err := finish(ti, &ti.Status, r.reconcile(ctx, ti), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, ti); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *TunnelIngressReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

// DNSRecordName is the name of the DNSRecord created for a TunnelIngress.
func DNSRecordName(ti *v1alpha1.TunnelIngress) string { return ti.Name + "-cname" }

func (r *TunnelIngressReconciler) reconcile(ctx context.Context, ti *v1alpha1.TunnelIngress) error {
	if ti.Spec.Service.Name != "" && ti.Spec.Service.Port == 0 {
		return notReady(v1alpha1.ReasonInvalidSpec, "spec.service.port is required when spec.service.name is set", 0)
	}
	ti.Status.ServiceURL = ServiceURL(ti)

	tunnel := &v1alpha1.Tunnel{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ti.Namespace, Name: ti.Spec.TunnelRef.Name}, tunnel); err != nil {
		if apierrors.IsNotFound(err) {
			return notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("Tunnel %q not found in namespace %s", ti.Spec.TunnelRef.Name, ti.Namespace), requeueDependency)
		}
		return err
	}
	if tunnel.Status.TunnelID == "" {
		return notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("Tunnel %q has not been created on Cloudflare yet", tunnel.Name), requeueDependency)
	}
	ti.Status.TunnelID = tunnel.Status.TunnelID

	dnsReady := true
	if ti.Spec.DNS.Enabled == nil || *ti.Spec.DNS.Enabled {
		ready, err := r.ensureDNSRecord(ctx, ti, tunnel)
		if err != nil {
			return err
		}
		dnsReady = ready
	} else {
		ti.Status.DNSRecordName = ""
	}

	if !slices.Contains(tunnel.Status.SyncedHostnames, IngressKey(ti)) {
		return notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("waiting for Tunnel %q to push hostname %s", tunnel.Name, ti.Spec.Hostname), requeueDependency)
	}
	if !dnsReady {
		return notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("waiting for DNSRecord %s to become ready", ti.Status.DNSRecordName), requeueDependency)
	}
	setReady(ti, &ti.Status, v1alpha1.ReasonSynced, fmt.Sprintf("%s routes to %s through tunnel %s", ti.Spec.Hostname, ti.Status.ServiceURL, tunnel.Status.TunnelID))
	return nil
}

// ensureDNSRecord creates or updates the owned DNSRecord and reports whether it is Ready.
func (r *TunnelIngressReconciler) ensureDNSRecord(ctx context.Context, ti *v1alpha1.TunnelIngress, tunnel *v1alpha1.Tunnel) (bool, error) {
	zone := v1alpha1.ZoneRef{}
	if ti.Spec.DNS.Zone != nil {
		zone = *ti.Spec.DNS.Zone
	} else {
		cl, _, err := r.Clients.Resolve(ctx, tunnel.Spec.AccountRef)
		if err != nil {
			return false, err
		}
		z, err := cl.ZoneForHostname(ctx, ti.Spec.Hostname)
		if err != nil {
			return false, err
		}
		zone.ID = z.ID
	}
	rec := &v1alpha1.DNSRecord{}
	rec.Name, rec.Namespace = DNSRecordName(ti), ti.Namespace
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rec, func() error {
		rec.Labels = managedLabels(map[string]string{v1alpha1.LabelTunnel: tunnel.Name})
		rec.Spec = v1alpha1.DNSRecordSpec{
			AccountRef:     tunnel.Spec.AccountRef,
			Zone:           zone,
			Type:           "CNAME",
			Name:           ti.Spec.Hostname,
			Content:        tunnel.Status.TunnelID + ".cfargotunnel.com",
			TTL:            1,
			Proxied:        true,
			Comment:        ti.Spec.DNS.Comment,
			DeletionPolicy: ti.Spec.DNS.DeletionPolicy,
		}
		if rec.Spec.Comment == "" {
			rec.Spec.Comment = fmt.Sprintf("managed by cloudflare-operator (TunnelIngress %s/%s)", ti.Namespace, ti.Name)
		}
		return controllerutil.SetControllerReference(ti, rec, r.Scheme())
	})
	if err != nil {
		return false, fmt.Errorf("reconcile DNSRecord: %w", err)
	}
	ti.Status.DNSRecordName = rec.Name
	return isReady(rec, &rec.Status), nil
}

// SetupWithManager registers the controller.
func (r *TunnelIngressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TunnelIngress{}).
		Owns(&v1alpha1.DNSRecord{}).
		Watches(&v1alpha1.Tunnel{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			list := &v1alpha1.TunnelIngressList{}
			if err := r.List(ctx, list, client.InNamespace(o.GetNamespace()), client.MatchingFields{IndexTunnelRef: o.GetName()}); err != nil {
				return nil
			}
			reqs := make([]ctrl.Request, 0, len(list.Items))
			for _, ti := range list.Items {
				reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ti.Namespace, Name: ti.Name}})
			}
			return reqs
		})).
		Named("tunnelingress").
		Complete(r)
}

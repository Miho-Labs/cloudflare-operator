package controller

import (
	"context"
	"fmt"
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

// AccessGroupReconciler manages reusable Access groups.
type AccessGroupReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessgroups/finalizers,verbs=update

// Reconcile creates, adopts, updates or deletes the group.
func (r *AccessGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	g := &v1alpha1.AccessGroup{}
	if err := r.Get(ctx, req.NamespacedName, g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !g.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, g)
	}
	if !controllerutil.ContainsFinalizer(g, v1alpha1.Finalizer) {
		orig := g.DeepCopy()
		controllerutil.AddFinalizer(g, v1alpha1.Finalizer)
		if err := r.Patch(ctx, g, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := g.DeepCopy()
	res, err := finish(g, &g.Status, r.reconcile(ctx, g), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, g); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *AccessGroupReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

func groupName(g *v1alpha1.AccessGroup) string {
	if g.Spec.Name != "" {
		return g.Spec.Name
	}
	return g.Name
}

func (r *AccessGroupReconciler) reconcile(ctx context.Context, g *v1alpha1.AccessGroup) error {
	cl, _, err := r.Clients.Resolve(ctx, g.Spec.AccountRef)
	if err != nil {
		return err
	}
	rr := &ruleResolver{Client: r.Client, namespace: g.Namespace}
	desired := cloudflare.AccessGroup{Name: groupName(g)}
	if desired.Include, err = rr.rules(ctx, g.Spec.Include); err != nil {
		return err
	}
	if desired.Exclude, err = rr.rules(ctx, g.Spec.Exclude); err != nil {
		return err
	}
	if desired.Require, err = rr.rules(ctx, g.Spec.Require); err != nil {
		return err
	}

	var current *cloudflare.AccessGroup
	if g.Status.GroupID != "" {
		if current, err = cl.GetAccessGroup(ctx, g.Status.GroupID); err != nil {
			return err
		}
		if current == nil {
			r.Recorder.Eventf(g, corev1.EventTypeWarning, EventFailed, "group %s disappeared from Cloudflare, recreating", g.Status.GroupID)
			g.Status.GroupID = ""
		}
	}
	if current == nil {
		if current, err = cl.FindAccessGroupByName(ctx, desired.Name); err != nil {
			return err
		}
		if current != nil {
			r.Recorder.Eventf(g, corev1.EventTypeNormal, EventAdopted, "adopted existing Access group %s (%s)", current.Name, current.ID)
		}
	}
	switch {
	case current == nil:
		if current, err = cl.CreateAccessGroup(ctx, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(g, corev1.EventTypeNormal, EventCreated, "created Access group %s (%s)", current.Name, current.ID)
	case !current.Equal(desired):
		desired.ID = current.ID
		if current, err = cl.UpdateAccessGroup(ctx, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(g, corev1.EventTypeNormal, EventUpdated, "updated Access group %s", current.Name)
	}
	g.Status.GroupID = current.ID
	setReady(g, &g.Status, v1alpha1.ReasonSynced, fmt.Sprintf("Access group %s is in sync", current.Name))
	return nil
}

func (r *AccessGroupReconciler) finalize(ctx context.Context, g *v1alpha1.AccessGroup) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(g, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if deletionPolicy(g.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && g.Status.GroupID != "" {
		cl, _, err := r.Clients.Resolve(ctx, g.Spec.AccountRef)
		if err != nil {
			return r.finalizeError(ctx, g, err)
		}
		if err := cl.DeleteAccessGroup(ctx, g.Status.GroupID); err != nil {
			return r.finalizeError(ctx, g, err)
		}
		r.Recorder.Eventf(g, corev1.EventTypeNormal, EventDeleted, "deleted Access group %s", g.Status.GroupID)
	} else if g.Status.GroupID != "" {
		r.Recorder.Eventf(g, corev1.EventTypeNormal, EventRetained, "kept Access group %s (deletionPolicy=Retain)", g.Status.GroupID)
	}
	orig := g.DeepCopy()
	controllerutil.RemoveFinalizer(g, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, g, client.MergeFrom(orig)))
}

func (r *AccessGroupReconciler) finalizeError(ctx context.Context, g *v1alpha1.AccessGroup, err error) (ctrl.Result, error) {
	orig := g.DeepCopy()
	res, rerr := finish(g, &g.Status, err, r.resync())
	setNotReady(g, &g.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, g)
	return res, rerr
}

// SetupWithManager registers the controller.
func (r *AccessGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AccessGroup{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.AccessGroupList{}, o.GetName())
		})).
		Named("accessgroup").
		Complete(r)
}

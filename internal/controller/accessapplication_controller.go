package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// AccessApplicationReconciler manages self-hosted Access applications with inline policies.
type AccessApplicationReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessapplications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessapplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessapplications/finalizers,verbs=update
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessservicetokens,verbs=get;list;watch

// Reconcile creates, adopts, updates or deletes the application.
func (r *AccessApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	app := &v1alpha1.AccessApplication{}
	if err := r.Get(ctx, req.NamespacedName, app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !app.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, app)
	}
	if !controllerutil.ContainsFinalizer(app, v1alpha1.Finalizer) {
		orig := app.DeepCopy()
		controllerutil.AddFinalizer(app, v1alpha1.Finalizer)
		if err := r.Patch(ctx, app, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := app.DeepCopy()
	res, err := finish(app, &app.Status, r.reconcile(ctx, app), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, app); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *AccessApplicationReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

func (r *AccessApplicationReconciler) desired(ctx context.Context, app *v1alpha1.AccessApplication) (cloudflare.AccessApplication, error) {
	name := app.Spec.Name
	if name == "" {
		name = app.Name
	}
	d := cloudflare.AccessApplication{
		Name:                    name,
		Domain:                  app.Spec.Domain,
		SelfHostedDomains:       app.Spec.SelfHostedDomains,
		SessionDuration:         app.Spec.SessionDuration,
		AllowedIdps:             app.Spec.AllowedIdps,
		AutoRedirectToIdentity:  app.Spec.AutoRedirectToIdentity,
		AppLauncherVisible:      app.Spec.AppLauncherVisible,
		SkipInterstitial:        app.Spec.SkipInterstitial,
		ServiceAuth401Redirect:  app.Spec.ServiceAuth401Redirect,
		HTTPOnlyCookieAttribute: app.Spec.HTTPOnlyCookieAttribute,
		CustomDenyMessage:       app.Spec.CustomDenyMessage,
		CustomDenyURL:           app.Spec.CustomDenyURL,
		LogoURL:                 app.Spec.LogoURL,
	}
	if d.AllowedIdps == nil {
		d.AllowedIdps = []string{}
	}
	if c := app.Spec.CORSHeaders; c != nil {
		d.CORSHeaders = &cloudflare.CORSHeaders{
			AllowAllHeaders: c.AllowAllHeaders, AllowAllMethods: c.AllowAllMethods, AllowAllOrigins: c.AllowAllOrigins,
			AllowCredentials: c.AllowCredentials, AllowedHeaders: c.AllowedHeaders, AllowedMethods: c.AllowedMethods,
			AllowedOrigins: c.AllowedOrigins, MaxAge: c.MaxAge,
		}
	}
	rr := &ruleResolver{Client: r.Client, namespace: app.Namespace}
	for i, p := range app.Spec.Policies {
		pol := cloudflare.AccessPolicy{
			Name:                         p.Name,
			Decision:                     p.Decision,
			Precedence:                   i + 1,
			SessionDuration:              p.SessionDuration,
			PurposeJustificationRequired: p.PurposeJustificationRequired,
			PurposeJustificationPrompt:   p.PurposeJustificationPrompt,
		}
		var err error
		if pol.Include, err = rr.rules(ctx, p.Include); err != nil {
			return d, err
		}
		if pol.Exclude, err = rr.rules(ctx, p.Exclude); err != nil {
			return d, err
		}
		if pol.Require, err = rr.rules(ctx, p.Require); err != nil {
			return d, err
		}
		d.Policies = append(d.Policies, pol)
	}
	return d, nil
}

func (r *AccessApplicationReconciler) reconcile(ctx context.Context, app *v1alpha1.AccessApplication) error {
	cl, _, err := r.Clients.Resolve(ctx, app.Spec.AccountRef)
	if err != nil {
		return err
	}
	desired, err := r.desired(ctx, app)
	if err != nil {
		return err
	}
	var current *cloudflare.AccessApplication
	if app.Status.ApplicationID != "" {
		if current, err = cl.GetAccessApplication(ctx, app.Status.ApplicationID); err != nil {
			return err
		}
		if current == nil {
			r.Recorder.Eventf(app, corev1.EventTypeWarning, EventFailed, "application %s disappeared from Cloudflare, recreating", app.Status.ApplicationID)
			app.Status.ApplicationID = ""
		}
	}
	if current == nil {
		if current, err = cl.FindAccessApplicationByDomain(ctx, desired.Domain); err != nil {
			return err
		}
		if current != nil {
			r.Recorder.Eventf(app, corev1.EventTypeNormal, EventAdopted, "adopted existing Access application for %s (%s)", current.Domain, current.ID)
		}
	}
	switch {
	case current == nil:
		if current, err = cl.CreateAccessApplication(ctx, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(app, corev1.EventTypeNormal, EventCreated, "created Access application %s (%s)", current.Name, current.ID)
	case !current.Equal(desired):
		desired.ID = current.ID
		if current, err = cl.UpdateAccessApplication(ctx, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(app, corev1.EventTypeNormal, EventUpdated, "updated Access application %s", current.Name)
	}
	app.Status.ApplicationID = current.ID
	app.Status.AUD = current.AUD
	app.Status.PolicyCount = len(current.Policies)
	setReady(app, &app.Status, v1alpha1.ReasonSynced, fmt.Sprintf("Access application %s protects %s with %d policies", current.Name, current.Domain, len(current.Policies)))
	return nil
}

func (r *AccessApplicationReconciler) finalize(ctx context.Context, app *v1alpha1.AccessApplication) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(app, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if deletionPolicy(app.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && app.Status.ApplicationID != "" {
		cl, _, err := r.Clients.Resolve(ctx, app.Spec.AccountRef)
		if err != nil {
			return r.finalizeError(ctx, app, err)
		}
		if err := cl.DeleteAccessApplication(ctx, app.Status.ApplicationID); err != nil {
			return r.finalizeError(ctx, app, err)
		}
		r.Recorder.Eventf(app, corev1.EventTypeNormal, EventDeleted, "deleted Access application %s", app.Status.ApplicationID)
	} else if app.Status.ApplicationID != "" {
		r.Recorder.Eventf(app, corev1.EventTypeNormal, EventRetained, "kept Access application %s (deletionPolicy=Retain)", app.Status.ApplicationID)
	}
	orig := app.DeepCopy()
	controllerutil.RemoveFinalizer(app, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, app, client.MergeFrom(orig)))
}

func (r *AccessApplicationReconciler) finalizeError(ctx context.Context, app *v1alpha1.AccessApplication, err error) (ctrl.Result, error) {
	orig := app.DeepCopy()
	res, rerr := finish(app, &app.Status, err, r.resync())
	setNotReady(app, &app.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, app)
	return res, rerr
}

// SetupWithManager registers the controller. Applications are re-reconciled when a group or service
// token in the same namespace changes, since policies reference them by name.
func (r *AccessApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	sameNamespace := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
		list := &v1alpha1.AccessApplicationList{}
		if err := r.List(ctx, list, client.InNamespace(o.GetNamespace())); err != nil {
			return nil
		}
		reqs := make([]ctrl.Request, 0, len(list.Items))
		for _, a := range list.Items {
			reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
		return reqs
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AccessApplication{}).
		Watches(&v1alpha1.AccessGroup{}, sameNamespace).
		Watches(&v1alpha1.AccessServiceToken{}, sameNamespace).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.AccessApplicationList{}, o.GetName())
		})).
		Named("accessapplication").
		Complete(r)
}

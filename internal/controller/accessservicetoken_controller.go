package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// AccessServiceTokenReconciler manages Access service tokens and their credential Secrets.
type AccessServiceTokenReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessservicetokens,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessservicetokens/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=accessservicetokens/finalizers,verbs=update

// Reconcile creates the token, stores the credentials and refreshes it before expiry.
func (r *AccessServiceTokenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	t := &v1alpha1.AccessServiceToken{}
	if err := r.Get(ctx, req.NamespacedName, t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !t.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, t)
	}
	if !controllerutil.ContainsFinalizer(t, v1alpha1.Finalizer) {
		orig := t.DeepCopy()
		controllerutil.AddFinalizer(t, v1alpha1.Finalizer)
		if err := r.Patch(ctx, t, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := t.DeepCopy()
	res, err := finish(t, &t.Status, r.reconcile(ctx, t), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, t); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *AccessServiceTokenReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

func serviceTokenName(t *v1alpha1.AccessServiceToken) string {
	if t.Spec.Name != "" {
		return t.Spec.Name
	}
	return t.Name
}

// ServiceTokenSecretName returns the Secret receiving the client ID and secret.
func ServiceTokenSecretName(t *v1alpha1.AccessServiceToken) string {
	if t.Spec.SecretName != "" {
		return t.Spec.SecretName
	}
	return t.Name + "-access-token"
}

func (r *AccessServiceTokenReconciler) reconcile(ctx context.Context, t *v1alpha1.AccessServiceToken) error {
	cl, _, err := r.Clients.Resolve(ctx, t.Spec.AccountRef)
	if err != nil {
		return err
	}
	secretName := ServiceTokenSecretName(t)
	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: t.Namespace, Name: secretName}, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	secretOK := err == nil && len(secret.Data["CF_ACCESS_CLIENT_SECRET"]) > 0 && string(secret.Data["CF_ACCESS_CLIENT_ID"]) == t.Status.ClientID

	var current *cloudflare.ServiceToken
	if t.Status.TokenID != "" {
		if current, err = cl.GetServiceToken(ctx, t.Status.TokenID); err != nil {
			return err
		}
		if current == nil {
			r.Recorder.Eventf(t, corev1.EventTypeWarning, EventFailed, "service token %s disappeared from Cloudflare, recreating", t.Status.TokenID)
			t.Status.TokenID = ""
		}
	}
	if current == nil {
		// Adopt by name only when we can rotate it to learn the secret.
		if current, err = cl.FindServiceTokenByName(ctx, serviceTokenName(t)); err != nil {
			return err
		}
		if current != nil {
			r.Recorder.Eventf(t, corev1.EventTypeNormal, EventAdopted, "adopted existing service token %s (%s); rotating its secret", current.Name, current.ID)
			if current, err = cl.RotateServiceToken(ctx, current.ID); err != nil {
				return err
			}
			secretOK = false
		}
	}
	if current == nil {
		if current, err = cl.CreateServiceToken(ctx, serviceTokenName(t), t.Spec.Duration); err != nil {
			return err
		}
		r.Recorder.Eventf(t, corev1.EventTypeNormal, EventCreated, "created service token %s (%s)", current.Name, current.ID)
		secretOK = false
	}
	if !secretOK && current.ClientSecret == "" {
		// The secret is lost (Secret deleted) and the API never returns it again: rotate.
		r.Recorder.Eventf(t, corev1.EventTypeWarning, EventUpdated, "credentials Secret %s missing; rotating service token secret", secretName)
		if current, err = cl.RotateServiceToken(ctx, current.ID); err != nil {
			return err
		}
	}
	if current.ClientSecret != "" {
		if err := r.writeSecret(ctx, t, secretName, current); err != nil {
			return err
		}
	}

	// Refresh before expiry.
	refreshBefore := 30 * 24 * time.Hour
	if t.Spec.RefreshBefore != "" {
		if d, perr := time.ParseDuration(t.Spec.RefreshBefore); perr == nil {
			refreshBefore = d
		}
	}
	// Never refresh more often than half the token lifetime.
	if lifetime, perr := time.ParseDuration(current.Duration); perr == nil && lifetime > 0 && refreshBefore > lifetime/2 {
		refreshBefore = lifetime / 2
	}
	if !current.ExpiresAt.IsZero() && time.Until(current.ExpiresAt) < refreshBefore {
		refreshed, err := cl.RefreshServiceToken(ctx, current.ID)
		if err != nil {
			return err
		}
		current.ExpiresAt = refreshed.ExpiresAt
		r.Recorder.Eventf(t, corev1.EventTypeNormal, EventUpdated, "refreshed service token %s, now expires %s", current.ID, current.ExpiresAt.Format(time.RFC3339))
	}

	t.Status.TokenID = current.ID
	t.Status.ClientID = current.ClientID
	t.Status.SecretName = secretName
	if !current.ExpiresAt.IsZero() {
		t.Status.ExpiresAt = &metav1.Time{Time: current.ExpiresAt}
	}
	setReady(t, &t.Status, v1alpha1.ReasonSynced, fmt.Sprintf("service token %s is in sync", current.Name))
	return nil
}

func (r *AccessServiceTokenReconciler) writeSecret(ctx context.Context, t *v1alpha1.AccessServiceToken, name string, tok *cloudflare.ServiceToken) error {
	secret := &corev1.Secret{}
	secret.Name, secret.Namespace = name, t.Namespace
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = managedLabels(nil)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			"CF_ACCESS_CLIENT_ID":     []byte(tok.ClientID),
			"CF_ACCESS_CLIENT_SECRET": []byte(tok.ClientSecret),
		}
		return controllerutil.SetControllerReference(t, secret, r.Scheme())
	})
	if err != nil {
		return fmt.Errorf("write credentials secret: %w", err)
	}
	return nil
}

func (r *AccessServiceTokenReconciler) finalize(ctx context.Context, t *v1alpha1.AccessServiceToken) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(t, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if deletionPolicy(t.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && t.Status.TokenID != "" {
		cl, _, err := r.Clients.Resolve(ctx, t.Spec.AccountRef)
		if err != nil {
			return r.finalizeError(ctx, t, err)
		}
		if err := cl.DeleteServiceToken(ctx, t.Status.TokenID); err != nil {
			return r.finalizeError(ctx, t, err)
		}
		r.Recorder.Eventf(t, corev1.EventTypeNormal, EventDeleted, "deleted service token %s", t.Status.TokenID)
	} else if t.Status.TokenID != "" {
		r.Recorder.Eventf(t, corev1.EventTypeNormal, EventRetained, "kept service token %s (deletionPolicy=Retain)", t.Status.TokenID)
	}
	orig := t.DeepCopy()
	controllerutil.RemoveFinalizer(t, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, t, client.MergeFrom(orig)))
}

func (r *AccessServiceTokenReconciler) finalizeError(ctx context.Context, t *v1alpha1.AccessServiceToken, err error) (ctrl.Result, error) {
	orig := t.DeepCopy()
	res, rerr := finish(t, &t.Status, err, r.resync())
	setNotReady(t, &t.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, t)
	return res, rerr
}

// SetupWithManager registers the controller.
func (r *AccessServiceTokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AccessServiceToken{}).
		Owns(&corev1.Secret{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.AccessServiceTokenList{}, o.GetName())
		})).
		Named("accessservicetoken").
		Complete(r)
}

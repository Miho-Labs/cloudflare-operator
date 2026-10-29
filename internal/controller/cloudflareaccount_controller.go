package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// CloudflareAccountReconciler verifies that an account's token works.
type CloudflareAccountReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=cloudflareaccounts,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=cloudflareaccounts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile verifies the credentials and records the account name.
func (r *CloudflareAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	account := &v1alpha1.CloudflareAccount{}
	if err := r.Get(ctx, req.NamespacedName, account); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := account.DeepCopy()
	res, err := r.reconcile(ctx, account)
	if perr := patchStatus(ctx, r.Client, orig, account); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *CloudflareAccountReconciler) reconcile(ctx context.Context, account *v1alpha1.CloudflareAccount) (ctrl.Result, error) {
	resync := r.ResyncPeriod
	if resync == 0 {
		resync = 30 * time.Minute
	}
	cl, err := r.Clients.ForAccount(ctx, account)
	if err != nil {
		setSyncFailed(account, &account.Status, v1alpha1.ReasonInvalidSpec, err.Error())
		return ctrl.Result{RequeueAfter: requeueAccount}, nil
	}
	info, err := cl.VerifyAccount(ctx)
	if err != nil {
		if cloudflare.IsAuthError(err) {
			msg := fmt.Sprintf("token rejected for account %s: %s", account.Spec.AccountID, cloudflare.Message(err))
			setSyncFailed(account, &account.Status, v1alpha1.ReasonAPIError, msg)
			r.Recorder.Event(account, corev1.EventTypeWarning, EventFailed, msg)
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
		}
		return finish(account, &account.Status, err, resync)
	}
	account.Status.AccountName = info.Name
	if tok, err := cl.VerifyToken(ctx); err == nil {
		account.Status.TokenStatus = tok.Status
	} else {
		account.Status.TokenStatus = "unknown"
	}
	now := metav1.Now()
	account.Status.LastVerified = &now
	setReady(account, &account.Status, v1alpha1.ReasonSynced, "credentials verified")
	return ctrl.Result{RequeueAfter: resync}, nil
}

// SetupWithManager registers the controller. The account is re-verified when its Secret changes.
func (r *CloudflareAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.CloudflareAccount{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.accountsForSecret)).
		Named("cloudflareaccount").
		Complete(r)
}

func (r *CloudflareAccountReconciler) accountsForSecret(ctx context.Context, obj client.Object) []ctrl.Request {
	list := &v1alpha1.CloudflareAccountList{}
	if err := r.List(ctx, list, client.MatchingFields{IndexSecretRef: obj.GetNamespace() + "/" + obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(list.Items))
	for _, a := range list.Items {
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Name: a.Name}})
	}
	return reqs
}

// SecretRefIndexer extracts the "namespace/name" of the token Secret for the field index.
func SecretRefIndexer(obj client.Object) []string {
	a, ok := obj.(*v1alpha1.CloudflareAccount)
	if !ok {
		return nil
	}
	return []string{a.Spec.APITokenSecretRef.Namespace + "/" + a.Spec.APITokenSecretRef.Name}
}

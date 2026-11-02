package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
)

// R2AccessKeyReconciler manages API tokens that grant S3 access to one or more R2 buckets.
type R2AccessKeyReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2accesskeys,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2accesskeys/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2accesskeys/finalizers,verbs=update
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2buckets,verbs=get;list;watch

// Reconcile creates the token and the credentials Secret.
func (r *R2AccessKeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := &v1alpha1.R2AccessKey{}
	if err := r.Get(ctx, req.NamespacedName, key); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !key.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, key)
	}
	if !controllerutil.ContainsFinalizer(key, v1alpha1.Finalizer) {
		orig := key.DeepCopy()
		controllerutil.AddFinalizer(key, v1alpha1.Finalizer)
		if err := r.Patch(ctx, key, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := key.DeepCopy()
	res, err := finish(key, &key.Status, r.reconcile(ctx, key), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, key); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *R2AccessKeyReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

// AccessKeySecretName returns the Secret that receives the credentials.
func AccessKeySecretName(k *v1alpha1.R2AccessKey) string {
	if k.Spec.SecretName != "" {
		return k.Spec.SecretName
	}
	return k.Name + "-r2-credentials"
}

func accessKeyTokenName(k *v1alpha1.R2AccessKey) string {
	if k.Spec.TokenName != "" {
		return k.Spec.TokenName
	}
	return fmt.Sprintf("k8s-%s-%s", k.Namespace, k.Name)
}

// resolveBuckets turns the bucket targets into "name[@jurisdiction]" entries, waiting for referenced
// R2Bucket objects to exist on Cloudflare.
func (r *R2AccessKeyReconciler) resolveBuckets(ctx context.Context, k *v1alpha1.R2AccessKey) ([]string, error) {
	out := make([]string, 0, len(k.Spec.Buckets))
	for _, t := range k.Spec.Buckets {
		if t.Ref == nil {
			out = append(out, bucketTarget(t.Name, t.Jurisdiction))
			continue
		}
		b := &v1alpha1.R2Bucket{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: k.Namespace, Name: t.Ref.Name}, b); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("R2Bucket %q not found", t.Ref.Name), requeueDependency)
			}
			return nil, err
		}
		if b.Status.BucketName == "" {
			return nil, notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("R2Bucket %q has not been created on Cloudflare yet", t.Ref.Name), requeueDependency)
		}
		out = append(out, bucketTarget(b.Status.BucketName, jurisdiction(b)))
	}
	sort.Strings(out)
	return out, nil
}

func (r *R2AccessKeyReconciler) reconcile(ctx context.Context, k *v1alpha1.R2AccessKey) error {
	cl, _, err := r.Clients.Resolve(ctx, k.Spec.AccountRef)
	if err != nil {
		return err
	}
	perm := k.Spec.Permission
	if perm == "" {
		perm = v1alpha1.R2PermissionReadWrite
	}
	var buckets []string
	if perm != v1alpha1.R2PermissionAdmin {
		if buckets, err = r.resolveBuckets(ctx, k); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(buckets))
	for _, b := range buckets {
		name, _, _ := strings.Cut(b, "@")
		names = append(names, name)
	}
	req := r2TokenRequest{
		owner:      k,
		secretName: AccessKeySecretName(k),
		tokenName:  accessKeyTokenName(k),
		permission: perm,
		buckets:    buckets,
		extraData:  map[string]string{"BUCKET_NAMES": strings.Join(names, ",")},
	}
	if len(names) == 1 {
		req.extraData["BUCKET_NAME"] = names[0]
	}
	id, err := ensureR2Token(ctx, r.Client, r.Recorder, cl, req, k.Status.TokenID)
	if err != nil {
		return err
	}
	k.Status.TokenID = id
	k.Status.Buckets = names
	k.Status.Endpoint = cl.R2Endpoint("")
	k.Status.SecretName = req.secretName
	scope := strings.Join(names, ", ")
	if perm == v1alpha1.R2PermissionAdmin {
		scope = "all buckets in the account"
	}
	setReady(k, &k.Status, v1alpha1.ReasonSynced, fmt.Sprintf("%s credentials for %s stored in Secret %s", perm, scope, req.secretName))
	return nil
}

func (r *R2AccessKeyReconciler) finalize(ctx context.Context, k *v1alpha1.R2AccessKey) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(k, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if k.Status.TokenID != "" {
		cl, _, err := r.Clients.Resolve(ctx, k.Spec.AccountRef)
		if err != nil {
			return r.finalizeError(ctx, k, err)
		}
		if err := cl.DeleteAccountToken(ctx, k.Status.TokenID); err != nil {
			return r.finalizeError(ctx, k, err)
		}
		r.Recorder.Eventf(k, corev1.EventTypeNormal, EventDeleted, "deleted API token %s", k.Status.TokenID)
	}
	orig := k.DeepCopy()
	controllerutil.RemoveFinalizer(k, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, k, client.MergeFrom(orig)))
}

func (r *R2AccessKeyReconciler) finalizeError(ctx context.Context, k *v1alpha1.R2AccessKey, err error) (ctrl.Result, error) {
	orig := k.DeepCopy()
	res, rerr := finish(k, &k.Status, err, r.resync())
	setNotReady(k, &k.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, k)
	return res, rerr
}

// SetupWithManager registers the controller. Keys are re-reconciled when an R2Bucket in the same
// namespace changes, since targets reference buckets by name.
func (r *R2AccessKeyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.R2AccessKey{}).
		Owns(&corev1.Secret{}).
		Watches(&v1alpha1.R2Bucket{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			list := &v1alpha1.R2AccessKeyList{}
			if err := r.List(ctx, list, client.InNamespace(o.GetNamespace())); err != nil {
				return nil
			}
			reqs := make([]ctrl.Request, 0, len(list.Items))
			for _, k := range list.Items {
				reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: k.Namespace, Name: k.Name}})
			}
			return reqs
		})).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.R2AccessKeyList{}, o.GetName())
		})).
		Named("r2accesskey").
		Complete(r)
}

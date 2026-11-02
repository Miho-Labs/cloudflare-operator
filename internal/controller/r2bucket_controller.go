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

// R2BucketReconciler manages R2 buckets and their S3 credentials.
type R2BucketReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2buckets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=r2buckets/finalizers,verbs=update

// Reconcile creates the bucket, the API token and the credentials Secret.
func (r *R2BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bucket := &v1alpha1.R2Bucket{}
	if err := r.Get(ctx, req.NamespacedName, bucket); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !bucket.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, bucket)
	}
	if !controllerutil.ContainsFinalizer(bucket, v1alpha1.Finalizer) {
		orig := bucket.DeepCopy()
		controllerutil.AddFinalizer(bucket, v1alpha1.Finalizer)
		if err := r.Patch(ctx, bucket, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := bucket.DeepCopy()
	res, err := finish(bucket, &bucket.Status, r.reconcile(ctx, bucket), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, bucket); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *R2BucketReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

func bucketName(b *v1alpha1.R2Bucket) string {
	if b.Spec.Name != "" {
		return b.Spec.Name
	}
	return b.Name
}

func jurisdiction(b *v1alpha1.R2Bucket) string {
	if b.Spec.Jurisdiction == "" || b.Spec.Jurisdiction == "default" {
		return ""
	}
	return b.Spec.Jurisdiction
}

// CredentialsSecretName returns the Secret that receives the S3 credentials.
func CredentialsSecretName(b *v1alpha1.R2Bucket) string {
	if b.Spec.APIToken.SecretName != "" {
		return b.Spec.APIToken.SecretName
	}
	return b.Name + "-r2-credentials"
}

func tokenName(b *v1alpha1.R2Bucket) string {
	if b.Spec.APIToken.TokenName != "" {
		return b.Spec.APIToken.TokenName
	}
	return fmt.Sprintf("k8s-%s-%s", b.Namespace, b.Name)
}

func (r *R2BucketReconciler) reconcile(ctx context.Context, bucket *v1alpha1.R2Bucket) error {
	cl, _, err := r.Clients.Resolve(ctx, bucket.Spec.AccountRef)
	if err != nil {
		return err
	}
	name := bucketName(bucket)
	existing, err := cl.GetR2Bucket(ctx, name, jurisdiction(bucket))
	if err != nil {
		return err
	}
	if existing == nil {
		existing, err = cl.CreateR2Bucket(ctx, cloudflare.R2BucketInput{
			Name:         name,
			LocationHint: bucket.Spec.LocationHint,
			StorageClass: bucket.Spec.StorageClass,
			Jurisdiction: jurisdiction(bucket),
		})
		if err != nil {
			return err
		}
		r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventCreated, "created R2 bucket %s", name)
	} else if bucket.Status.BucketName == "" {
		r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventAdopted, "adopted existing R2 bucket %s", name)
	}
	bucket.Status.BucketName = existing.Name
	bucket.Status.Endpoint = cl.R2Endpoint(bucket.Spec.Jurisdiction)

	if bucket.Spec.APIToken.Enabled {
		if err := r.ensureToken(ctx, cl, bucket); err != nil {
			return err
		}
	} else if bucket.Status.TokenID != "" {
		if err := cl.DeleteAccountToken(ctx, bucket.Status.TokenID); err != nil {
			return err
		}
		r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventDeleted, "deleted API token %s (apiToken disabled)", bucket.Status.TokenID)
		bucket.Status.TokenID = ""
		bucket.Status.CredentialsSecretName = ""
	}
	setReady(bucket, &bucket.Status, v1alpha1.ReasonSynced, fmt.Sprintf("bucket %s is in sync", name))
	return nil
}

func (r *R2BucketReconciler) ensureToken(ctx context.Context, cl *cloudflare.Client, bucket *v1alpha1.R2Bucket) error {
	req := r2TokenRequest{
		owner:      bucket,
		secretName: CredentialsSecretName(bucket),
		tokenName:  tokenName(bucket),
		permission: bucket.Spec.APIToken.Permission,
		buckets:    []string{bucketTarget(bucketName(bucket), jurisdiction(bucket))},
		extraData: map[string]string{
			"AWS_ENDPOINT_URL": bucket.Status.Endpoint,
			"BUCKET_NAME":      bucket.Status.BucketName,
		},
	}
	id, err := ensureR2Token(ctx, r.Client, r.Recorder, cl, req, bucket.Status.TokenID)
	if err != nil {
		return err
	}
	bucket.Status.TokenID = id
	bucket.Status.CredentialsSecretName = req.secretName
	return nil
}

// bucketTarget encodes a bucket and its jurisdiction for r2TokenRequest.buckets.
func bucketTarget(name, jurisdiction string) string {
	if jurisdiction == "" || jurisdiction == "default" {
		return name
	}
	return name + "@" + jurisdiction
}

func (r *R2BucketReconciler) finalize(ctx context.Context, bucket *v1alpha1.R2Bucket) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(bucket, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	cl, _, err := r.Clients.Resolve(ctx, bucket.Spec.AccountRef)
	if err != nil {
		return r.finalizeError(ctx, bucket, err)
	}
	if bucket.Status.TokenID != "" {
		if err := cl.DeleteAccountToken(ctx, bucket.Status.TokenID); err != nil {
			return r.finalizeError(ctx, bucket, err)
		}
		r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventDeleted, "deleted API token %s", bucket.Status.TokenID)
	}
	if deletionPolicy(bucket.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && bucket.Status.BucketName != "" {
		if err := cl.DeleteR2Bucket(ctx, bucket.Status.BucketName, jurisdiction(bucket)); err != nil {
			if cloudflare.IsConflict(err) {
				err = fmt.Errorf("bucket %s is not empty; empty it or set deletionPolicy=Retain: %w", bucket.Status.BucketName, err)
			}
			return r.finalizeError(ctx, bucket, err)
		}
		r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventDeleted, "deleted R2 bucket %s", bucket.Status.BucketName)
	} else if bucket.Status.BucketName != "" {
		r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventRetained, "kept R2 bucket %s (deletionPolicy=Retain)", bucket.Status.BucketName)
	}
	orig := bucket.DeepCopy()
	bucket.Status.TokenID = ""
	controllerutil.RemoveFinalizer(bucket, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, bucket, client.MergeFrom(orig)))
}

func (r *R2BucketReconciler) finalizeError(ctx context.Context, bucket *v1alpha1.R2Bucket, err error) (ctrl.Result, error) {
	orig := bucket.DeepCopy()
	res, rerr := finish(bucket, &bucket.Status, err, r.resync())
	setNotReady(bucket, &bucket.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, bucket)
	if cloudflare.IsConflict(err) {
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	return res, rerr
}

// SetupWithManager registers the controller.
func (r *R2BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.R2Bucket{}).
		Owns(&corev1.Secret{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.R2BucketList{}, o.GetName())
		})).
		Named("r2bucket").
		Complete(r)
}

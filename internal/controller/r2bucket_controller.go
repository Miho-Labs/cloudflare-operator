package controller

import (
	"context"
	"fmt"
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
	secretName := CredentialsSecretName(bucket)
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: bucket.Namespace, Name: secretName}, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	secretOK := err == nil && len(secret.Data["AWS_SECRET_ACCESS_KEY"]) > 0 && string(secret.Data["AWS_ACCESS_KEY_ID"]) == bucket.Status.TokenID

	if bucket.Status.TokenID != "" {
		exists, err := cl.AccountTokenExists(ctx, bucket.Status.TokenID)
		if err != nil {
			return err
		}
		if exists && secretOK && secret.Annotations[annotationPermission] == string(bucket.Spec.APIToken.Permission) {
			bucket.Status.CredentialsSecretName = secretName
			return nil
		}
		// The token value cannot be read back, so any mismatch means a new token.
		if exists {
			if err := cl.DeleteAccountToken(ctx, bucket.Status.TokenID); err != nil {
				return err
			}
		}
		bucket.Status.TokenID = ""
	}

	policies, err := r.tokenPolicies(ctx, cl, bucket)
	if err != nil {
		return err
	}
	created, err := cl.CreateAccountToken(ctx, tokenName(bucket), policies)
	if err != nil {
		return err
	}
	r.Recorder.Eventf(bucket, corev1.EventTypeNormal, EventCreated, "created API token %s (%s)", tokenName(bucket), created.ID)

	secret = &corev1.Secret{}
	secret.Name, secret.Namespace = secretName, bucket.Namespace
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = managedLabels(nil)
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[annotationPermission] = string(bucket.Spec.APIToken.Permission)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte(created.ID),
			"AWS_SECRET_ACCESS_KEY": []byte(cloudflare.R2SecretAccessKey(created.Value)),
			"AWS_ENDPOINT_URL":      []byte(bucket.Status.Endpoint),
			"AWS_REGION":            []byte("auto"),
			"BUCKET_NAME":           []byte(bucket.Status.BucketName),
			"R2_ACCOUNT_ID":         []byte(cl.AccountID),
			"R2_TOKEN_ID":           []byte(created.ID),
		}
		return controllerutil.SetControllerReference(bucket, secret, r.Scheme())
	})
	if err != nil {
		// Do not leak a token nobody can use.
		_ = cl.DeleteAccountToken(ctx, created.ID)
		return fmt.Errorf("write credentials secret: %w", err)
	}
	bucket.Status.TokenID = created.ID
	bucket.Status.CredentialsSecretName = secretName
	return nil
}

const annotationPermission = v1alpha1.Group + "/permission"

func (r *R2BucketReconciler) tokenPolicies(ctx context.Context, cl *cloudflare.Client, bucket *v1alpha1.R2Bucket) ([]cloudflare.TokenPolicy, error) {
	perm := bucket.Spec.APIToken.Permission
	if perm == "" {
		perm = v1alpha1.R2PermissionReadWrite
	}
	var groups []string
	resources := map[string]string{cl.R2BucketResource(bucketName(bucket), jurisdiction(bucket)): "*"}
	switch perm {
	case v1alpha1.R2PermissionReadOnly:
		groups = []string{cloudflare.PermR2BucketItemRead}
	case v1alpha1.R2PermissionReadWrite:
		groups = []string{cloudflare.PermR2BucketItemRead, cloudflare.PermR2BucketItemWrite}
	case v1alpha1.R2PermissionAdmin:
		groups = []string{cloudflare.PermR2StorageWrite}
		resources = map[string]string{cl.AccountResource(): "*"}
	default:
		return nil, notReady(v1alpha1.ReasonInvalidSpec, fmt.Sprintf("unknown apiToken.permission %q", perm), 0)
	}
	ids, err := cl.PermissionGroupIDs(ctx, groups...)
	if err != nil {
		return nil, err
	}
	groupIDs := make([]string, 0, len(groups))
	for _, g := range groups {
		groupIDs = append(groupIDs, ids[g])
	}
	return []cloudflare.TokenPolicy{{PermissionGroupIDs: groupIDs, Resources: resources}}, nil
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

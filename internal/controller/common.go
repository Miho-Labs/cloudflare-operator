// Package controller holds one reconciler per CRD plus the helpers they share.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// Field index names registered in cmd/main.go and in tests.
const (
	IndexAccountRef = ".spec.accountRef.name"
	IndexTunnelRef  = ".spec.tunnelRef.name"
	IndexSecretRef  = ".spec.apiTokenSecretRef"
)

// Requeue intervals.
const (
	DefaultResyncPeriod = 10 * time.Minute
	requeueRateLimited  = time.Minute
	requeueDependency   = 15 * time.Second
	requeueAccount      = 30 * time.Second
)

// Event reasons.
const (
	EventCreated  = "Created"
	EventUpdated  = "Updated"
	EventDeleted  = "Deleted"
	EventAdopted  = "Adopted"
	EventRetained = "Retained"
	EventFailed   = "Failed"
)

// Conditions is implemented by every status struct embedding v1alpha1.ResourceStatus.
type Conditions interface {
	GetConditions() []metav1.Condition
	SetConditions([]metav1.Condition)
	SetObservedGeneration(int64)
}

// setCondition sets a condition, keeping LastTransitionTime stable when the status did not change.
func setCondition(obj client.Object, st Conditions, condType string, status metav1.ConditionStatus, reason, message string) {
	conds := st.GetConditions()
	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: obj.GetGeneration(),
	})
	st.SetConditions(conds)
	st.SetObservedGeneration(obj.GetGeneration())
}

func setReady(obj client.Object, st Conditions, reason, message string) {
	setCondition(obj, st, v1alpha1.ConditionReady, metav1.ConditionTrue, reason, message)
	setCondition(obj, st, v1alpha1.ConditionSynced, metav1.ConditionTrue, v1alpha1.ReasonSynced, "last sync with Cloudflare succeeded")
}

func setNotReady(obj client.Object, st Conditions, reason, message string) {
	setCondition(obj, st, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, message)
}

func setSyncFailed(obj client.Object, st Conditions, reason, message string) {
	setCondition(obj, st, v1alpha1.ConditionSynced, metav1.ConditionFalse, reason, message)
	setCondition(obj, st, v1alpha1.ConditionReady, metav1.ConditionFalse, reason, message)
}

// isReady reports whether the Ready condition is True for the current generation.
func isReady(obj client.Object, st Conditions) bool {
	c := meta.FindStatusCondition(st.GetConditions(), v1alpha1.ConditionReady)
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration == obj.GetGeneration()
}

// patchStatus writes the status subresource with a merge patch against orig.
func patchStatus(ctx context.Context, c client.Client, orig, obj client.Object) error {
	if err := c.Status().Patch(ctx, obj, client.MergeFrom(orig)); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// errNotReady signals that a dependency is not ready yet; the reconcile requeues without backoff.
type errNotReady struct {
	reason  string
	message string
	after   time.Duration
}

func (e *errNotReady) Error() string { return e.message }

func notReady(reason, message string, after time.Duration) error {
	return &errNotReady{reason: reason, message: message, after: after}
}

// finish turns an error from a reconcile body into (condition, result, error).
// Dependency errors requeue quietly; rate limits requeue after a minute; anything else sets
// Synced=False and returns the error so controller-runtime backs off.
func finish(obj client.Object, st Conditions, err error, resync time.Duration) (ctrl.Result, error) {
	if err == nil {
		return ctrl.Result{RequeueAfter: resync}, nil
	}
	var nr *errNotReady
	if errors.As(err, &nr) {
		setNotReady(obj, st, nr.reason, nr.message)
		return ctrl.Result{RequeueAfter: nr.after}, nil
	}
	if cloudflare.IsRateLimited(err) {
		setSyncFailed(obj, st, v1alpha1.ReasonAPIError, "rate limited by Cloudflare: "+cloudflare.Message(err))
		return ctrl.Result{RequeueAfter: requeueRateLimited}, nil
	}
	reason := v1alpha1.ReasonAPIError
	if cloudflare.StatusCode(err) == 0 {
		reason = v1alpha1.ReasonReconciling
	}
	setSyncFailed(obj, st, reason, cloudflare.Message(err))
	return ctrl.Result{}, err
}

// ClientFactory builds Cloudflare clients from CloudflareAccount resources and caches them.
type ClientFactory struct {
	client.Client
	// BaseURL overrides every account's API endpoint (tests).
	BaseURL   string
	UserAgent string

	mu    sync.Mutex
	cache map[string]*cloudflare.Client
}

// NewClientFactory creates a factory.
func NewClientFactory(c client.Client) *ClientFactory {
	return &ClientFactory{Client: c, cache: map[string]*cloudflare.Client{}, UserAgent: "cloudflare-operator"}
}

// Credentials reads the account and its token Secret.
func (f *ClientFactory) Credentials(ctx context.Context, account *v1alpha1.CloudflareAccount) (token, baseURL string, err error) {
	ref := account.Spec.APITokenSecretRef
	if ref.Namespace == "" {
		return "", "", fmt.Errorf("spec.apiTokenSecretRef.namespace is required on CloudflareAccount %s", account.Name)
	}
	key := ref.Key
	if key == "" {
		key = "apiToken"
	}
	secret := &corev1.Secret{}
	if err := f.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		return "", "", fmt.Errorf("read secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	val, ok := secret.Data[key]
	if !ok || len(val) == 0 {
		return "", "", fmt.Errorf("secret %s/%s has no key %q", ref.Namespace, ref.Name, key)
	}
	baseURL = f.BaseURL
	if baseURL == "" {
		baseURL = account.Spec.APIBaseURL
	}
	return string(val), baseURL, nil
}

// ForAccount returns a client for the named CloudflareAccount.
func (f *ClientFactory) ForAccount(ctx context.Context, account *v1alpha1.CloudflareAccount) (*cloudflare.Client, error) {
	token, baseURL, err := f.Credentials(ctx, account)
	if err != nil {
		return nil, err
	}
	hash := cloudflare.CredentialsHash(token, account.Spec.AccountID, baseURL)
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cache[hash]; ok {
		return c, nil
	}
	c := cloudflare.New(cloudflare.Options{APIToken: token, AccountID: account.Spec.AccountID, BaseURL: baseURL, UserAgent: f.UserAgent})
	f.cache[hash] = c
	return c, nil
}

// Resolve looks up the CloudflareAccount behind an accountRef and returns a client for it.
// Returns an errNotReady when the account is missing, not yet verified, or failing.
func (f *ClientFactory) Resolve(ctx context.Context, ref v1alpha1.AccountRef) (*cloudflare.Client, *v1alpha1.CloudflareAccount, error) {
	account := &v1alpha1.CloudflareAccount{}
	if err := f.Get(ctx, types.NamespacedName{Name: ref.Name}, account); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, notReady(v1alpha1.ReasonAccountNotFound, fmt.Sprintf("CloudflareAccount %q not found", ref.Name), requeueAccount)
		}
		return nil, nil, err
	}
	if c := meta.FindStatusCondition(account.Status.Conditions, v1alpha1.ConditionReady); c != nil && c.Status == metav1.ConditionFalse {
		return nil, nil, notReady(v1alpha1.ReasonAccountNotReady, fmt.Sprintf("CloudflareAccount %q is not ready: %s", ref.Name, c.Message), requeueAccount)
	}
	cl, err := f.ForAccount(ctx, account)
	if err != nil {
		return nil, nil, notReady(v1alpha1.ReasonAccountNotReady, err.Error(), requeueAccount)
	}
	return cl, account, nil
}

// requestsForAccount maps a CloudflareAccount to every object in list that references it.
func requestsForAccount(ctx context.Context, c client.Client, list client.ObjectList, accountName string) []ctrl.Request {
	if err := c.List(ctx, list, client.MatchingFields{IndexAccountRef: accountName}); err != nil {
		return nil
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(items))
	for _, it := range items {
		o, ok := it.(client.Object)
		if !ok {
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}})
	}
	return reqs
}

// managedLabels are set on every Kubernetes object the operator creates.
func managedLabels(extra map[string]string) map[string]string {
	l := map[string]string{v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue}
	for k, v := range extra {
		l[k] = v
	}
	return l
}

func deletionPolicy(p v1alpha1.DeletionPolicy) v1alpha1.DeletionPolicy {
	if p == "" {
		return v1alpha1.DeletionPolicyDelete
	}
	return p
}

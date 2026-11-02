package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

const annotationPolicyHash = v1alpha1.Group + "/policy-hash"

// r2TokenRequest describes the account API token an owner wants and where its credentials go.
type r2TokenRequest struct {
	owner      client.Object
	secretName string
	tokenName  string
	permission v1alpha1.R2TokenPermission
	// buckets are "name" or "name@jurisdiction" entries; ignored for admin.
	buckets []string
	// extraData is merged into the Secret (e.g. BUCKET_NAME).
	extraData map[string]string
}

// policyHash identifies the token's scope so a changed permission or bucket set triggers rotation.
func (req r2TokenRequest) policyHash() string {
	parts := append([]string{string(req.permission)}, req.buckets...)
	sort.Strings(parts[1:])
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}

// r2TokenPolicies translates the request into token policies, resolving permission-group IDs.
func r2TokenPolicies(ctx context.Context, cl *cloudflare.Client, req r2TokenRequest) ([]cloudflare.TokenPolicy, error) {
	perm := req.permission
	if perm == "" {
		perm = v1alpha1.R2PermissionReadWrite
	}
	var groups []string
	resources := map[string]string{}
	switch perm {
	case v1alpha1.R2PermissionReadOnly:
		groups = []string{cloudflare.PermR2BucketItemRead}
	case v1alpha1.R2PermissionReadWrite:
		groups = []string{cloudflare.PermR2BucketItemRead, cloudflare.PermR2BucketItemWrite}
	case v1alpha1.R2PermissionAdmin:
		groups = []string{cloudflare.PermR2StorageWrite}
		resources[cl.AccountResource()] = "*"
	default:
		return nil, notReady(v1alpha1.ReasonInvalidSpec, fmt.Sprintf("unknown permission %q", perm), 0)
	}
	if perm != v1alpha1.R2PermissionAdmin {
		if len(req.buckets) == 0 {
			return nil, notReady(v1alpha1.ReasonInvalidSpec, "at least one bucket is required unless permission is admin", 0)
		}
		for _, b := range req.buckets {
			name, jurisdiction, _ := strings.Cut(b, "@")
			resources[cl.R2BucketResource(name, jurisdiction)] = "*"
		}
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

// ensureR2Token makes sure an account API token matching req exists and its credentials are in the
// Secret. currentTokenID is the token recorded in status ("" when none). It returns the token ID to
// record. The token value is only ever returned at creation, so any mismatch (missing Secret, changed
// scope, vanished token) results in a new token and the old one being deleted.
func ensureR2Token(ctx context.Context, c client.Client, rec record.EventRecorder, cl *cloudflare.Client, req r2TokenRequest, currentTokenID string) (string, error) {
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Namespace: req.owner.GetNamespace(), Name: req.secretName}, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	hash := req.policyHash()
	secretOK := err == nil && len(secret.Data["AWS_SECRET_ACCESS_KEY"]) > 0 &&
		string(secret.Data["AWS_ACCESS_KEY_ID"]) == currentTokenID && secret.Annotations[annotationPolicyHash] == hash

	if currentTokenID != "" {
		exists, err := cl.AccountTokenExists(ctx, currentTokenID)
		if err != nil {
			return "", err
		}
		if exists && secretOK {
			return currentTokenID, nil
		}
		if exists {
			if err := cl.DeleteAccountToken(ctx, currentTokenID); err != nil {
				return "", err
			}
			rec.Eventf(req.owner, corev1.EventTypeNormal, EventDeleted, "deleted API token %s (credentials Secret missing or scope changed)", currentTokenID)
		} else {
			rec.Eventf(req.owner, corev1.EventTypeWarning, EventFailed, "API token %s disappeared from Cloudflare, recreating", currentTokenID)
		}
	}

	policies, err := r2TokenPolicies(ctx, cl, req)
	if err != nil {
		return "", err
	}
	created, err := cl.CreateAccountToken(ctx, req.tokenName, policies)
	if err != nil {
		return "", err
	}
	rec.Eventf(req.owner, corev1.EventTypeNormal, EventCreated, "created API token %s (%s)", req.tokenName, created.ID)

	data := map[string][]byte{
		"AWS_ACCESS_KEY_ID":     []byte(created.ID),
		"AWS_SECRET_ACCESS_KEY": []byte(cloudflare.R2SecretAccessKey(created.Value)),
		"AWS_ENDPOINT_URL":      []byte(cl.R2Endpoint("")),
		"AWS_REGION":            []byte("auto"),
		"R2_ACCOUNT_ID":         []byte(cl.AccountID),
		"R2_TOKEN_ID":           []byte(created.ID),
	}
	for k, v := range req.extraData {
		data[k] = []byte(v)
	}
	secret = &corev1.Secret{}
	secret.Name, secret.Namespace = req.secretName, req.owner.GetNamespace()
	_, err = controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		secret.Labels = managedLabels(nil)
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[annotationPolicyHash] = hash
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return controllerutil.SetControllerReference(req.owner, secret, c.Scheme())
	})
	if err != nil {
		// Do not leak a token nobody can use.
		_ = cl.DeleteAccountToken(ctx, created.ID)
		return "", fmt.Errorf("write credentials secret: %w", err)
	}
	return created.ID, nil
}

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// R2BucketTarget selects a bucket an R2AccessKey grants access to. Exactly one of ref or name is set.
// +kubebuilder:validation:XValidation:rule="has(self.ref) != has(self.name)",message="exactly one of ref or name must be set"
type R2BucketTarget struct {
	// Ref points at an R2Bucket resource in the same namespace; its bucket name and jurisdiction are used.
	// +optional
	Ref *LocalObjectRef `json:"ref,omitempty"`
	// Name of a bucket that exists on Cloudflare (not necessarily managed by the operator).
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`
	// +optional
	Name string `json:"name,omitempty"`
	// Jurisdiction of the bucket named by name (default, eu, fedramp).
	// +kubebuilder:validation:Enum=default;eu;fedramp
	// +optional
	Jurisdiction string `json:"jurisdiction,omitempty"`
}

// R2AccessKeySpec defines S3-compatible credentials for one or more R2 buckets.
// +kubebuilder:validation:XValidation:rule="self.permission == 'admin' || size(self.buckets) > 0",message="buckets is required unless permission is admin"
type R2AccessKeySpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Permission granted on the listed buckets. admin grants access to every bucket in the account.
	// +kubebuilder:default=read-write
	// +optional
	Permission R2TokenPermission `json:"permission,omitempty"`
	// Buckets the key can access. Ignored for permission admin.
	// +optional
	Buckets []R2BucketTarget `json:"buckets,omitempty"`
	// SecretName of the generated Secret. Defaults to "<name>-r2-credentials".
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// TokenName on Cloudflare. Defaults to "k8s-<namespace>-<name>".
	// +optional
	TokenName string `json:"tokenName,omitempty"`
	// DeletionPolicy is accepted for consistency; the API token is always deleted with the object
	// because its secret cannot be recovered.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// R2AccessKeyStatus is the observed state of an R2AccessKey.
type R2AccessKeyStatus struct {
	ResourceStatus `json:",inline"`
	// TokenID of the API token (also the S3 access key ID).
	// +optional
	TokenID string `json:"tokenID,omitempty"`
	// Buckets the current token is scoped to.
	// +optional
	Buckets []string `json:"buckets,omitempty"`
	// Endpoint is the S3-compatible endpoint for the account.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// SecretName holding the credentials.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=r2key
// +kubebuilder:printcolumn:name="Permission",type=string,JSONPath=`.spec.permission`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// R2AccessKey manages an API token with S3 credentials for one or more R2 buckets.
type R2AccessKey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   R2AccessKeySpec   `json:"spec,omitempty"`
	Status R2AccessKeyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// R2AccessKeyList contains a list of R2AccessKey.
type R2AccessKeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []R2AccessKey `json:"items"`
}

func init() {
	SchemeBuilder.Register(&R2AccessKey{}, &R2AccessKeyList{})
}

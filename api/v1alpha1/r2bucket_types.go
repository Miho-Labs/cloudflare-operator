package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// R2TokenPermission is the access level granted by the generated API token.
// +kubebuilder:validation:Enum=read-only;read-write;admin
type R2TokenPermission string

const (
	// R2PermissionReadOnly allows reading objects in the bucket.
	R2PermissionReadOnly R2TokenPermission = "read-only"
	// R2PermissionReadWrite allows reading and writing objects in the bucket.
	R2PermissionReadWrite R2TokenPermission = "read-write"
	// R2PermissionAdmin allows managing all R2 buckets in the account.
	R2PermissionAdmin R2TokenPermission = "admin"
)

// R2APITokenSpec requests an S3-compatible credential scoped to the bucket.
type R2APITokenSpec struct {
	// Enabled creates an account-owned API token and writes S3 credentials to a Secret.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// SecretName of the generated Secret. Defaults to "<name>-r2-credentials".
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// Permission granted to the token.
	// +kubebuilder:default=read-write
	// +optional
	Permission R2TokenPermission `json:"permission,omitempty"`
	// TokenName on Cloudflare. Defaults to "k8s-<namespace>-<name>".
	// +optional
	TokenName string `json:"tokenName,omitempty"`
}

// R2BucketSpec defines an R2 bucket.
type R2BucketSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Name of the bucket. Defaults to metadata.name.
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`
	// +optional
	Name string `json:"name,omitempty"`
	// LocationHint suggests where the bucket data is stored (apac, eeur, enam, weur, wnam, oc).
	// +kubebuilder:validation:Enum=apac;eeur;enam;weur;wnam;oc
	// +optional
	LocationHint string `json:"locationHint,omitempty"`
	// StorageClass for new objects.
	// +kubebuilder:validation:Enum=Standard;InfrequentAccess
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
	// Jurisdiction restricts where data is stored (default, eu, fedramp).
	// +kubebuilder:validation:Enum=default;eu;fedramp
	// +kubebuilder:default=default
	// +optional
	Jurisdiction string `json:"jurisdiction,omitempty"`
	// APIToken requests S3 credentials for this bucket.
	// +optional
	APIToken R2APITokenSpec `json:"apiToken,omitempty"`
	// DeletionPolicy controls whether the bucket is removed from Cloudflare on delete.
	// A bucket must be empty before Cloudflare lets it be deleted.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// R2BucketStatus is the observed state of an R2Bucket.
type R2BucketStatus struct {
	ResourceStatus `json:",inline"`
	// BucketName on Cloudflare.
	// +optional
	BucketName string `json:"bucketName,omitempty"`
	// Endpoint is the S3-compatible endpoint for this account.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// TokenID of the generated API token.
	// +optional
	TokenID string `json:"tokenID,omitempty"`
	// CredentialsSecretName holds the S3 credentials.
	// +optional
	CredentialsSecretName string `json:"credentialsSecretName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=r2
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.status.bucketName`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.credentialsSecretName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// R2Bucket manages an R2 bucket and optionally an API token with S3 credentials.
type R2Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   R2BucketSpec   `json:"spec,omitempty"`
	Status R2BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// R2BucketList contains a list of R2Bucket.
type R2BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []R2Bucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&R2Bucket{}, &R2BucketList{})
}

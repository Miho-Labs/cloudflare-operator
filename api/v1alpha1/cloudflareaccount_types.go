package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CloudflareAccountSpec defines how to reach one Cloudflare account.
type CloudflareAccountSpec struct {
	// AccountID is the Cloudflare account identifier (32 hex characters). Shown in the dashboard sidebar
	// of any zone and in the dashboard URL (dash.cloudflare.com/<accountID>).
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{32}$`
	AccountID string `json:"accountID"`
	// APITokenSecretRef points at the Secret key holding the API token.
	APITokenSecretRef SecretKeyRef `json:"apiTokenSecretRef"`
	// APIBaseURL overrides the Cloudflare API endpoint. Mostly useful for testing.
	// +optional
	APIBaseURL string `json:"apiBaseURL,omitempty"`
}

// CloudflareAccountStatus is the observed state of a CloudflareAccount.
type CloudflareAccountStatus struct {
	ResourceStatus `json:",inline"`
	// AccountName as reported by Cloudflare.
	// +optional
	AccountName string `json:"accountName,omitempty"`
	// TokenStatus as reported by the token verify endpoint (active, disabled, expired).
	// +optional
	TokenStatus string `json:"tokenStatus,omitempty"`
	// LastVerified is when the credentials were last checked successfully.
	// +optional
	LastVerified *metav1.Time `json:"lastVerified,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cfaccount
// +kubebuilder:printcolumn:name="Account ID",type=string,JSONPath=`.spec.accountID`
// +kubebuilder:printcolumn:name="Account",type=string,JSONPath=`.status.accountName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CloudflareAccount holds the credentials every other resource uses through accountRef.
type CloudflareAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareAccountSpec   `json:"spec,omitempty"`
	Status CloudflareAccountStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudflareAccountList contains a list of CloudflareAccount.
type CloudflareAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareAccount{}, &CloudflareAccountList{})
}

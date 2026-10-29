package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AccessRuleRef points at an AccessGroup/AccessServiceToken resource (by name, same namespace) or
// directly at a Cloudflare object ID. Exactly one must be set.
// +kubebuilder:validation:XValidation:rule="has(self.name) != has(self.id)",message="exactly one of name or id must be set"
type AccessRuleRef struct {
	// Name of the Kubernetes resource in the same namespace.
	// +optional
	Name string `json:"name,omitempty"`
	// ID of the Cloudflare object.
	// +optional
	ID string `json:"id,omitempty"`
}

// GitHubOrganizationRule matches members of a GitHub organization.
type GitHubOrganizationRule struct {
	// Name of the GitHub organization.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// IdentityProviderID of the GitHub identity provider configured in Zero Trust.
	// +kubebuilder:validation:MinLength=1
	IdentityProviderID string `json:"identityProviderID"`
	// Teams restricts the match to these team slugs.
	// +optional
	Teams []string `json:"teams,omitempty"`
}

// AccessRule is one selector in an include/exclude/require list. Every set field becomes one
// Cloudflare rule; rules inside the same list are OR-ed.
type AccessRule struct {
	// Emails matches exact email addresses.
	// +optional
	Emails []string `json:"emails,omitempty"`
	// EmailDomains matches every address of these domains.
	// +optional
	EmailDomains []string `json:"emailDomains,omitempty"`
	// Everyone matches any user.
	// +optional
	Everyone bool `json:"everyone,omitempty"`
	// IPs matches source IP ranges in CIDR notation.
	// +optional
	IPs []string `json:"ips,omitempty"`
	// Countries matches two-letter country codes.
	// +optional
	Countries []string `json:"countries,omitempty"`
	// LoginMethods matches identity provider IDs.
	// +optional
	LoginMethods []string `json:"loginMethods,omitempty"`
	// Groups matches members of Access groups.
	// +optional
	Groups []AccessRuleRef `json:"groups,omitempty"`
	// ServiceTokens matches specific service tokens.
	// +optional
	ServiceTokens []AccessRuleRef `json:"serviceTokens,omitempty"`
	// AnyValidServiceToken matches any service token of the account.
	// +optional
	AnyValidServiceToken bool `json:"anyValidServiceToken,omitempty"`
	// GitHubOrganizations matches members of GitHub organizations.
	// +optional
	GitHubOrganizations []GitHubOrganizationRule `json:"githubOrganizations,omitempty"`
}

// AccessPolicy is one policy evaluated by an AccessApplication. Policies run in list order.
type AccessPolicy struct {
	// Name of the policy.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Decision taken when the policy matches.
	// +kubebuilder:validation:Enum=allow;deny;bypass;non_identity
	Decision string `json:"decision"`
	// Include rules: at least one must match.
	// +kubebuilder:validation:MinItems=1
	Include []AccessRule `json:"include"`
	// Exclude rules: none may match.
	// +optional
	Exclude []AccessRule `json:"exclude,omitempty"`
	// Require rules: all must match.
	// +optional
	Require []AccessRule `json:"require,omitempty"`
	// SessionDuration overrides the application session duration, e.g. "24h".
	// +optional
	SessionDuration string `json:"sessionDuration,omitempty"`
	// PurposeJustificationRequired asks users to justify their access.
	// +optional
	PurposeJustificationRequired bool `json:"purposeJustificationRequired,omitempty"`
	// PurposeJustificationPrompt is shown when justification is required.
	// +optional
	PurposeJustificationPrompt string `json:"purposeJustificationPrompt,omitempty"`
}

// CORSHeaders configures CORS for an application.
type CORSHeaders struct {
	// +optional
	AllowAllHeaders bool `json:"allowAllHeaders,omitempty"`
	// +optional
	AllowAllMethods bool `json:"allowAllMethods,omitempty"`
	// +optional
	AllowAllOrigins bool `json:"allowAllOrigins,omitempty"`
	// +optional
	AllowCredentials bool `json:"allowCredentials,omitempty"`
	// +optional
	AllowedHeaders []string `json:"allowedHeaders,omitempty"`
	// +optional
	AllowedMethods []string `json:"allowedMethods,omitempty"`
	// +optional
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`
	// MaxAge in seconds.
	// +optional
	MaxAge *int `json:"maxAge,omitempty"`
}

// AccessApplicationSpec defines a self-hosted Zero Trust Access application.
type AccessApplicationSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Name shown in the dashboard and App Launcher. Defaults to metadata.name.
	// +optional
	Name string `json:"name,omitempty"`
	// Domain protected by the application, e.g. "app.example.com" or "app.example.com/admin".
	// +kubebuilder:validation:MinLength=1
	Domain string `json:"domain"`
	// SelfHostedDomains lists additional domains protected by the same application.
	// +optional
	SelfHostedDomains []string `json:"selfHostedDomains,omitempty"`
	// SessionDuration before users must re-authenticate, e.g. "24h". Defaults to Cloudflare's 24h.
	// +optional
	SessionDuration string `json:"sessionDuration,omitempty"`
	// AllowedIdps restricts login to these identity provider IDs.
	// +optional
	AllowedIdps []string `json:"allowedIdps,omitempty"`
	// AutoRedirectToIdentity skips the identity provider selection page when only one is allowed.
	// +optional
	AutoRedirectToIdentity bool `json:"autoRedirectToIdentity,omitempty"`
	// AppLauncherVisible shows the application in the App Launcher.
	// +optional
	AppLauncherVisible *bool `json:"appLauncherVisible,omitempty"`
	// SkipInterstitial skips the "you are being redirected" page.
	// +optional
	SkipInterstitial bool `json:"skipInterstitial,omitempty"`
	// ServiceAuth401Redirect returns 401 instead of redirecting to the login page for service auth.
	// +optional
	ServiceAuth401Redirect bool `json:"serviceAuth401Redirect,omitempty"`
	// HTTPOnlyCookieAttribute sets the HttpOnly attribute on the Access cookie.
	// +optional
	HTTPOnlyCookieAttribute *bool `json:"httpOnlyCookieAttribute,omitempty"`
	// CustomDenyMessage shown to denied users.
	// +optional
	CustomDenyMessage string `json:"customDenyMessage,omitempty"`
	// CustomDenyURL denied users are redirected to.
	// +optional
	CustomDenyURL string `json:"customDenyURL,omitempty"`
	// LogoURL shown in the App Launcher.
	// +optional
	LogoURL string `json:"logoURL,omitempty"`
	// CORSHeaders configuration.
	// +optional
	CORSHeaders *CORSHeaders `json:"corsHeaders,omitempty"`
	// Policies evaluated in order. Groups and service tokens referenced by name must be Ready.
	// +kubebuilder:validation:MinItems=1
	Policies []AccessPolicy `json:"policies"`
	// DeletionPolicy controls whether the application is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// AccessApplicationStatus is the observed state of an AccessApplication.
type AccessApplicationStatus struct {
	ResourceStatus `json:",inline"`
	// ApplicationID on Cloudflare.
	// +optional
	ApplicationID string `json:"applicationID,omitempty"`
	// AUD is the application audience tag used to validate Access JWTs.
	// +optional
	AUD string `json:"aud,omitempty"`
	// PolicyCount is the number of policies attached.
	// +optional
	PolicyCount int `json:"policyCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfapp
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="App ID",type=string,JSONPath=`.status.applicationID`
// +kubebuilder:printcolumn:name="Policies",type=integer,JSONPath=`.status.policyCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AccessApplication manages a self-hosted Zero Trust Access application with inline policies.
type AccessApplication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AccessApplicationSpec   `json:"spec,omitempty"`
	Status AccessApplicationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AccessApplicationList contains a list of AccessApplication.
type AccessApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessApplication `json:"items"`
}

// AccessGroupSpec defines a reusable Access group.
type AccessGroupSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Name of the group on Cloudflare. Defaults to metadata.name.
	// +optional
	Name string `json:"name,omitempty"`
	// Include rules: at least one must match.
	// +kubebuilder:validation:MinItems=1
	Include []AccessRule `json:"include"`
	// Exclude rules: none may match.
	// +optional
	Exclude []AccessRule `json:"exclude,omitempty"`
	// Require rules: all must match.
	// +optional
	Require []AccessRule `json:"require,omitempty"`
	// DeletionPolicy controls whether the group is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// AccessGroupStatus is the observed state of an AccessGroup.
type AccessGroupStatus struct {
	ResourceStatus `json:",inline"`
	// GroupID on Cloudflare.
	// +optional
	GroupID string `json:"groupID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfgroup
// +kubebuilder:printcolumn:name="Group ID",type=string,JSONPath=`.status.groupID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AccessGroup manages a reusable Zero Trust Access group.
type AccessGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AccessGroupSpec   `json:"spec,omitempty"`
	Status AccessGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AccessGroupList contains a list of AccessGroup.
type AccessGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessGroup `json:"items"`
}

// AccessServiceTokenSpec defines a service token for machine-to-machine access.
type AccessServiceTokenSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Name of the token on Cloudflare. Defaults to metadata.name.
	// +optional
	Name string `json:"name,omitempty"`
	// Duration the token is valid for, e.g. "8760h" (one year). Defaults to Cloudflare's one year.
	// +kubebuilder:validation:Pattern=`^[0-9]+h$`
	// +optional
	Duration string `json:"duration,omitempty"`
	// SecretName of the generated Secret (keys CF_ACCESS_CLIENT_ID and CF_ACCESS_CLIENT_SECRET).
	// Defaults to "<name>-access-token".
	// +optional
	SecretName string `json:"secretName,omitempty"`
	// RefreshBefore extends the token when it expires within this window, e.g. "720h".
	// +kubebuilder:default="720h"
	// +kubebuilder:validation:Pattern=`^[0-9]+h$`
	// +optional
	RefreshBefore string `json:"refreshBefore,omitempty"`
	// DeletionPolicy controls whether the token is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// AccessServiceTokenStatus is the observed state of an AccessServiceToken.
type AccessServiceTokenStatus struct {
	ResourceStatus `json:",inline"`
	// TokenID on Cloudflare.
	// +optional
	TokenID string `json:"tokenID,omitempty"`
	// ClientID of the token.
	// +optional
	ClientID string `json:"clientID,omitempty"`
	// ExpiresAt is when the token stops being accepted.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// SecretName holding the credentials.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfservicetoken
// +kubebuilder:printcolumn:name="Client ID",type=string,JSONPath=`.status.clientID`
// +kubebuilder:printcolumn:name="Expires",type=string,JSONPath=`.status.expiresAt`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AccessServiceToken manages a Zero Trust service token and stores its credentials in a Secret.
type AccessServiceToken struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AccessServiceTokenSpec   `json:"spec,omitempty"`
	Status AccessServiceTokenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AccessServiceTokenList contains a list of AccessServiceToken.
type AccessServiceTokenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessServiceToken `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&AccessApplication{}, &AccessApplicationList{},
		&AccessGroup{}, &AccessGroupList{},
		&AccessServiceToken{}, &AccessServiceTokenList{},
	)
}

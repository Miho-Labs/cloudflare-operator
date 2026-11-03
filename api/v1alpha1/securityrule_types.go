package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RulePosition places a rule inside its ruleset. Rules are evaluated in order.
type RulePosition struct {
	// Index is the 1-based position in the ruleset. Omitted appends the rule at the end.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Index *int `json:"index,omitempty"`
}

// BlockResponse customises the response returned for a blocking action.
type BlockResponse struct {
	// StatusCode of the response (400-499).
	// +kubebuilder:validation:Minimum=400
	// +kubebuilder:validation:Maximum=499
	StatusCode int `json:"statusCode"`
	// ContentType of the body, e.g. "text/plain" or "application/json".
	// +kubebuilder:default="text/plain"
	// +optional
	ContentType string `json:"contentType,omitempty"`
	// Body returned to the client.
	// +optional
	Body string `json:"body,omitempty"`
}

// RulesetRuleStatus is shared by every resource that manages one rule in a zone ruleset.
type RulesetRuleStatus struct {
	ResourceStatus `json:",inline"`
	// ZoneID the rule lives in.
	// +optional
	ZoneID string `json:"zoneID,omitempty"`
	// RulesetID of the zone's phase entrypoint ruleset.
	// +optional
	RulesetID string `json:"rulesetID,omitempty"`
	// RuleID inside the ruleset.
	// +optional
	RuleID string `json:"ruleID,omitempty"`
	// RuleVersion last observed.
	// +optional
	RuleVersion string `json:"ruleVersion,omitempty"`
}

// RateLimitParameters configure how requests are counted.
type RateLimitParameters struct {
	// Characteristics the counter is keyed on, e.g. ip.src, cf.colo.id, http.request.headers["x-api-key"].
	// +kubebuilder:default={"ip.src","cf.colo.id"}
	// +optional
	Characteristics []string `json:"characteristics,omitempty"`
	// Period in seconds the requests are counted over. Free plans only allow 10.
	// +kubebuilder:validation:Enum=10;60;120;300;600;3600
	// +kubebuilder:default=10
	// +optional
	Period int `json:"period,omitempty"`
	// RequestsPerPeriod before the action triggers.
	// +kubebuilder:validation:Minimum=1
	RequestsPerPeriod int `json:"requestsPerPeriod"`
	// MitigationTimeout in seconds the action stays applied. Defaults to the period.
	// +optional
	MitigationTimeout int `json:"mitigationTimeout,omitempty"`
	// CountingExpression restricts which requests are counted (defaults to the rule expression).
	// +optional
	CountingExpression string `json:"countingExpression,omitempty"`
	// RequestsToOrigin counts only requests that reach the origin (cache misses).
	// +optional
	RequestsToOrigin bool `json:"requestsToOrigin,omitempty"`
}

// RateLimitRuleSpec defines a rate limiting rule in the zone's http_ratelimit ruleset.
type RateLimitRuleSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Zone the rule applies to.
	Zone ZoneRef `json:"zone"`
	// Description shown in the dashboard. Also the key used to adopt an existing rule.
	// +kubebuilder:validation:MinLength=1
	Description string `json:"description"`
	// Expression selecting the requests to rate limit, e.g. (http.request.uri.path wildcard "/api/*").
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
	// Enabled toggles the rule without deleting it.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Action taken when the limit is exceeded.
	// +kubebuilder:validation:Enum=block;managed_challenge;challenge;js_challenge;log
	// +kubebuilder:default=block
	// +optional
	Action string `json:"action,omitempty"`
	// BlockResponse customises the block response.
	// +optional
	BlockResponse *BlockResponse `json:"blockResponse,omitempty"`
	// RateLimit parameters.
	RateLimit RateLimitParameters `json:"rateLimit"`
	// Position of the rule in the ruleset.
	// +optional
	Position *RulePosition `json:"position,omitempty"`
	// DeletionPolicy controls whether the rule is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfratelimit
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone.name`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Limit",type=integer,JSONPath=`.spec.rateLimit.requestsPerPeriod`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RateLimitRule manages one rate limiting rule of a zone.
type RateLimitRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RateLimitRuleSpec `json:"spec,omitempty"`
	Status RulesetRuleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RateLimitRuleList contains a list of RateLimitRule.
type RateLimitRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RateLimitRule `json:"items"`
}

// SkipParameters configure what a "skip" action skips.
type SkipParameters struct {
	// Ruleset "current" skips the remaining custom rules; "all" skips all remaining rulesets.
	// +kubebuilder:validation:Enum=current;all
	// +optional
	Ruleset string `json:"ruleset,omitempty"`
	// Phases to skip, e.g. http_ratelimit, http_request_firewall_managed, http_request_sbfm.
	// +optional
	Phases []string `json:"phases,omitempty"`
	// Products to skip: zoneLockdown, uaBlock, bic, hot, securityLevel, rateLimit, waf.
	// +optional
	Products []string `json:"products,omitempty"`
}

// WAFRuleSpec defines a custom WAF rule in the zone's http_request_firewall_custom ruleset.
type WAFRuleSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Zone the rule applies to.
	Zone ZoneRef `json:"zone"`
	// Description shown in the dashboard. Also the key used to adopt an existing rule.
	// +kubebuilder:validation:MinLength=1
	Description string `json:"description"`
	// Expression selecting the requests, e.g. (ip.src.country eq "RU") or (cf.threat_score gt 30).
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
	// Enabled toggles the rule without deleting it.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Action taken for matching requests.
	// +kubebuilder:validation:Enum=block;challenge;managed_challenge;js_challenge;log;skip
	// +kubebuilder:default=block
	// +optional
	Action string `json:"action,omitempty"`
	// BlockResponse customises the block response.
	// +optional
	BlockResponse *BlockResponse `json:"blockResponse,omitempty"`
	// Skip parameters, used with action skip.
	// +optional
	Skip *SkipParameters `json:"skip,omitempty"`
	// Position of the rule in the ruleset.
	// +optional
	Position *RulePosition `json:"position,omitempty"`
	// DeletionPolicy controls whether the rule is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfwaf
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone.name`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WAFRule manages one custom WAF rule of a zone (Security → WAF → Custom rules).
type WAFRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WAFRuleSpec       `json:"spec,omitempty"`
	Status RulesetRuleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WAFRuleList contains a list of WAFRule.
type WAFRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WAFRule `json:"items"`
}

// ManagedRuleOverride overrides one rule of a managed ruleset.
type ManagedRuleOverride struct {
	// ID of the managed rule.
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
	// Action override.
	// +kubebuilder:validation:Enum=block;challenge;managed_challenge;js_challenge;log
	// +optional
	Action string `json:"action,omitempty"`
	// Enabled override.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// ScoreThreshold override (OWASP ruleset only).
	// +optional
	ScoreThreshold *int `json:"scoreThreshold,omitempty"`
}

// ManagedCategoryOverride overrides every rule of a category (tag) of a managed ruleset.
type ManagedCategoryOverride struct {
	// Category (tag) name, e.g. "wordpress" or "paranoia-level-2".
	// +kubebuilder:validation:MinLength=1
	Category string `json:"category"`
	// Action override.
	// +kubebuilder:validation:Enum=block;challenge;managed_challenge;js_challenge;log
	// +optional
	Action string `json:"action,omitempty"`
	// Enabled override.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// ManagedRulesetOverrides tune a managed ruleset deployment.
type ManagedRulesetOverrides struct {
	// Action applied to every rule of the ruleset unless overridden below.
	// +kubebuilder:validation:Enum=block;challenge;managed_challenge;js_challenge;log
	// +optional
	Action string `json:"action,omitempty"`
	// Enabled toggles every rule of the ruleset unless overridden below.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// ScoreThreshold for the OWASP Core Ruleset (default 40; lower is stricter).
	// +optional
	ScoreThreshold *int `json:"scoreThreshold,omitempty"`
	// ParanoiaLevel for the OWASP Core Ruleset (1-4). Levels above it are disabled.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	// +optional
	ParanoiaLevel *int `json:"paranoiaLevel,omitempty"`
	// Rules overrides individual rules.
	// +optional
	Rules []ManagedRuleOverride `json:"rules,omitempty"`
	// Categories overrides rule categories.
	// +optional
	Categories []ManagedCategoryOverride `json:"categories,omitempty"`
}

// ManagedWAFRuleSpec deploys a Cloudflare managed ruleset in the zone's
// http_request_firewall_managed ruleset.
type ManagedWAFRuleSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Zone the rule applies to.
	Zone ZoneRef `json:"zone"`
	// Description shown in the dashboard. Also the key used to adopt an existing rule.
	// +kubebuilder:validation:MinLength=1
	Description string `json:"description"`
	// Expression selecting the requests the managed ruleset runs on. Defaults to all requests.
	// +kubebuilder:default="true"
	// +optional
	Expression string `json:"expression,omitempty"`
	// Enabled toggles the rule without deleting it.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Ruleset to deploy. cloudflare-free-managed is available on every plan; the others need Pro or higher.
	// +kubebuilder:validation:Enum=cloudflare-managed;owasp-core;cloudflare-exposed-credentials-check;cloudflare-free-managed
	Ruleset string `json:"ruleset"`
	// Overrides tune the deployment.
	// +optional
	Overrides *ManagedRulesetOverrides `json:"overrides,omitempty"`
	// Position of the rule in the ruleset.
	// +optional
	Position *RulePosition `json:"position,omitempty"`
	// DeletionPolicy controls whether the rule is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// ManagedWAFRuleStatus is the observed state of a ManagedWAFRule.
type ManagedWAFRuleStatus struct {
	RulesetRuleStatus `json:",inline"`
	// ManagedRulesetID of the deployed Cloudflare ruleset.
	// +optional
	ManagedRulesetID string `json:"managedRulesetID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfmanagedwaf
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone.name`
// +kubebuilder:printcolumn:name="Ruleset",type=string,JSONPath=`.spec.ruleset`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ManagedWAFRule deploys a Cloudflare managed WAF ruleset on a zone.
type ManagedWAFRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedWAFRuleSpec   `json:"spec,omitempty"`
	Status ManagedWAFRuleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ManagedWAFRuleList contains a list of ManagedWAFRule.
type ManagedWAFRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedWAFRule `json:"items"`
}

// IPAccessRuleSpec defines an IP Access Rule (Security → WAF → Tools).
// +kubebuilder:validation:XValidation:rule="self.scope != 'zone' || has(self.zone)",message="zone is required when scope is zone"
type IPAccessRuleSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Scope: "zone" applies to one zone, "account" to every zone of the account.
	// +kubebuilder:validation:Enum=zone;account
	// +kubebuilder:default=zone
	// +optional
	Scope string `json:"scope,omitempty"`
	// Zone the rule applies to (scope zone).
	// +optional
	Zone *ZoneRef `json:"zone,omitempty"`
	// Mode of the rule.
	// +kubebuilder:validation:Enum=block;challenge;managed_challenge;js_challenge;whitelist
	Mode string `json:"mode"`
	// Target type of the value.
	// +kubebuilder:validation:Enum=ip;ip_range;asn;country
	Target string `json:"target"`
	// Value: an IP, a CIDR (/16 or /24 for IPv4, /32 to /64 for IPv6), an ASN like AS13335, or a country code.
	// +kubebuilder:validation:MinLength=1
	Value string `json:"value"`
	// Notes shown in the dashboard.
	// +optional
	Notes string `json:"notes,omitempty"`
	// DeletionPolicy controls whether the rule is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// IPAccessRuleStatus is the observed state of an IPAccessRule.
type IPAccessRuleStatus struct {
	ResourceStatus `json:",inline"`
	// RuleID on Cloudflare.
	// +optional
	RuleID string `json:"ruleID,omitempty"`
	// ZoneID the rule was created in (zone scope).
	// +optional
	ZoneID string `json:"zoneID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfipaccess
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.spec.scope`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Value",type=string,JSONPath=`.spec.value`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IPAccessRule manages one IP Access Rule at zone or account level.
type IPAccessRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IPAccessRuleSpec   `json:"spec,omitempty"`
	Status IPAccessRuleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IPAccessRuleList contains a list of IPAccessRule.
type IPAccessRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IPAccessRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&RateLimitRule{}, &RateLimitRuleList{},
		&WAFRule{}, &WAFRuleList{},
		&ManagedWAFRule{}, &ManagedWAFRuleList{},
		&IPAccessRule{}, &IPAccessRuleList{},
	)
}

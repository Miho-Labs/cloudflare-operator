package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TunnelIngressService describes where cloudflared forwards traffic. Set either url or name.
// +kubebuilder:validation:XValidation:rule="has(self.url) != has(self.name)",message="exactly one of service.url or service.name must be set"
type TunnelIngressService struct {
	// URL of the origin, e.g. "http://my-svc.default.svc.cluster.local:8080", "tcp://db:5432"
	// or "http_status:404". Mutually exclusive with name.
	// +optional
	URL string `json:"url,omitempty"`
	// Name of a Kubernetes Service in the same namespace. Mutually exclusive with url.
	// +optional
	Name string `json:"name,omitempty"`
	// Port of the Service. Required when name is set.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
	// Scheme used with name/port.
	// +kubebuilder:validation:Enum=http;https;tcp;ssh;rdp;smb;unix
	// +kubebuilder:default=http
	// +optional
	Scheme string `json:"scheme,omitempty"`
}

// TunnelIngressDNS controls the CNAME the operator creates for the hostname.
type TunnelIngressDNS struct {
	// Enabled creates a proxied CNAME record "<hostname> -> <tunnelID>.cfargotunnel.com".
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Zone the record belongs to. Defaults to the longest registered zone that matches the hostname.
	// +optional
	Zone *ZoneRef `json:"zone,omitempty"`
	// Comment attached to the DNS record.
	// +optional
	Comment string `json:"comment,omitempty"`
	// DeletionPolicy for the DNS record.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// TunnelIngressSpec defines one public hostname served by a Tunnel.
type TunnelIngressSpec struct {
	// TunnelRef selects the Tunnel (same namespace) this hostname is attached to.
	TunnelRef LocalObjectRef `json:"tunnelRef"`
	// Hostname served through the tunnel, e.g. "app.example.com".
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`
	// Path restricts the rule to requests whose path matches this regular expression.
	// +optional
	Path string `json:"path,omitempty"`
	// Service is the origin cloudflared forwards to.
	Service TunnelIngressService `json:"service"`
	// OriginRequest overrides the tunnel-wide origin settings for this hostname.
	// +optional
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
	// DNS controls the CNAME record for the hostname.
	// +optional
	DNS TunnelIngressDNS `json:"dns,omitempty"`
}

// TunnelIngressStatus is the observed state of a TunnelIngress.
type TunnelIngressStatus struct {
	ResourceStatus `json:",inline"`
	// ServiceURL is the resolved origin URL pushed to the tunnel configuration.
	// +optional
	ServiceURL string `json:"serviceURL,omitempty"`
	// TunnelID of the tunnel this rule is attached to.
	// +optional
	TunnelID string `json:"tunnelID,omitempty"`
	// DNSRecordName is the DNSRecord resource created for this hostname.
	// +optional
	DNSRecordName string `json:"dnsRecordName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfingress
// +kubebuilder:printcolumn:name="Tunnel",type=string,JSONPath=`.spec.tunnelRef.name`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.status.serviceURL`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TunnelIngress publishes one hostname (an "app") through a Tunnel.
type TunnelIngress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TunnelIngressSpec   `json:"spec,omitempty"`
	Status TunnelIngressStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TunnelIngressList contains a list of TunnelIngress.
type TunnelIngressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TunnelIngress `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TunnelIngress{}, &TunnelIngressList{})
}

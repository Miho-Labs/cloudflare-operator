package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OriginRequest holds the connection settings between cloudflared and the origin.
// Every field is optional; unset fields keep the cloudflared defaults.
type OriginRequest struct {
	// NoTLSVerify disables TLS verification of the origin certificate.
	// +optional
	NoTLSVerify *bool `json:"noTLSVerify,omitempty"`
	// HTTPHostHeader sets the Host header sent to the origin.
	// +optional
	HTTPHostHeader string `json:"httpHostHeader,omitempty"`
	// OriginServerName is the SNI hostname used when connecting to a TLS origin.
	// +optional
	OriginServerName string `json:"originServerName,omitempty"`
	// ConnectTimeout in seconds for establishing a connection to the origin.
	// +optional
	ConnectTimeout *int `json:"connectTimeout,omitempty"`
	// TLSTimeout in seconds for the TLS handshake with the origin.
	// +optional
	TLSTimeout *int `json:"tlsTimeout,omitempty"`
	// TCPKeepAlive in seconds.
	// +optional
	TCPKeepAlive *int `json:"tcpKeepAlive,omitempty"`
	// KeepAliveConnections is the maximum number of idle keep-alive connections.
	// +optional
	KeepAliveConnections *int `json:"keepAliveConnections,omitempty"`
	// KeepAliveTimeout in seconds before an idle keep-alive connection is closed.
	// +optional
	KeepAliveTimeout *int `json:"keepAliveTimeout,omitempty"`
	// DisableChunkedEncoding disables chunked transfer encoding to the origin.
	// +optional
	DisableChunkedEncoding *bool `json:"disableChunkedEncoding,omitempty"`
	// HTTP2Origin connects to the origin with HTTP/2.
	// +optional
	HTTP2Origin *bool `json:"http2Origin,omitempty"`
	// NoHappyEyeballs disables the "happy eyeballs" IPv4/IPv6 algorithm.
	// +optional
	NoHappyEyeballs *bool `json:"noHappyEyeballs,omitempty"`
	// ProxyType is "" for a plain proxy or "socks" for a SOCKS5 proxy.
	// +kubebuilder:validation:Enum="";socks
	// +optional
	ProxyType string `json:"proxyType,omitempty"`
	// CAPool is a path to a CA bundle inside the cloudflared container.
	// +optional
	CAPool string `json:"caPool,omitempty"`
}

// CloudflaredSpec configures the cloudflared Deployment the operator runs for a Tunnel.
type CloudflaredSpec struct {
	// Enabled controls whether the operator deploys cloudflared pods. When false you must run
	// cloudflared yourself with the token stored in status.tokenSecretName.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Replicas of cloudflared. Each replica opens 4 connections to Cloudflare.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// Image overrides the cloudflared container image.
	// +optional
	Image string `json:"image,omitempty"`
	// Protocol used between cloudflared and the edge (auto, quic, http2).
	// +kubebuilder:validation:Enum=auto;quic;http2
	// +kubebuilder:default=auto
	// +optional
	Protocol string `json:"protocol,omitempty"`
	// ExtraArgs are appended to the cloudflared command line.
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`
	// Resources for the cloudflared container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// NodeSelector for the cloudflared pods.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations for the cloudflared pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Affinity for the cloudflared pods. Defaults to soft pod anti-affinity across nodes.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// PodAnnotations added to the cloudflared pods.
	// +optional
	PodAnnotations map[string]string `json:"podAnnotations,omitempty"`
	// PriorityClassName for the cloudflared pods.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
}

// TunnelSpec defines a Cloudflare Tunnel and its in-cluster connector.
type TunnelSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Name of the tunnel on Cloudflare. Defaults to metadata.name. An existing tunnel with the
	// same name is adopted instead of creating a new one.
	// +optional
	Name string `json:"name,omitempty"`
	// Cloudflared configures the connector Deployment.
	// +optional
	Cloudflared CloudflaredSpec `json:"cloudflared,omitempty"`
	// OriginRequest sets tunnel-wide origin defaults; TunnelIngress can override per hostname.
	// +optional
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
	// CatchAllService is the service used for requests that match no TunnelIngress.
	// +kubebuilder:default="http_status:404"
	// +optional
	CatchAllService string `json:"catchAllService,omitempty"`
	// DeletionPolicy controls whether the tunnel is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// TunnelStatus is the observed state of a Tunnel.
type TunnelStatus struct {
	ResourceStatus `json:",inline"`
	// TunnelID on Cloudflare.
	// +optional
	TunnelID string `json:"tunnelID,omitempty"`
	// TunnelName on Cloudflare.
	// +optional
	TunnelName string `json:"tunnelName,omitempty"`
	// TokenSecretName is the Secret holding the connector token (key TUNNEL_TOKEN).
	// +optional
	TokenSecretName string `json:"tokenSecretName,omitempty"`
	// ConnectorStatus as reported by Cloudflare: inactive, degraded, healthy or down.
	// +optional
	ConnectorStatus string `json:"connectorStatus,omitempty"`
	// IngressCount is the number of TunnelIngress rules pushed to the tunnel configuration.
	// +optional
	IngressCount int `json:"ingressCount,omitempty"`
	// SyncedHostnames lists the "hostname[path]" entries currently in the remote configuration.
	// +optional
	SyncedHostnames []string `json:"syncedHostnames,omitempty"`
	// ConfigVersion is the version of the remote configuration last pushed.
	// +optional
	ConfigVersion int64 `json:"configVersion,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cftunnel
// +kubebuilder:printcolumn:name="Tunnel ID",type=string,JSONPath=`.status.tunnelID`
// +kubebuilder:printcolumn:name="Connector",type=string,JSONPath=`.status.connectorStatus`
// +kubebuilder:printcolumn:name="Ingresses",type=integer,JSONPath=`.status.ingressCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Tunnel manages a remotely-configured Cloudflare Tunnel and, by default, a cloudflared Deployment.
type Tunnel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TunnelSpec   `json:"spec,omitempty"`
	Status TunnelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TunnelList contains a list of Tunnel.
type TunnelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tunnel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tunnel{}, &TunnelList{})
}

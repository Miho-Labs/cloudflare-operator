package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ZoneRef selects a Cloudflare zone by name or ID. Exactly one must be set.
// +kubebuilder:validation:XValidation:rule="has(self.name) != has(self.id)",message="exactly one of zone.name or zone.id must be set"
type ZoneRef struct {
	// Name of the zone, e.g. "example.com".
	// +optional
	Name string `json:"name,omitempty"`
	// ID of the zone (32 hex characters).
	// +optional
	ID string `json:"id,omitempty"`
}

// DNSRecordSpec defines a DNS record in a zone.
type DNSRecordSpec struct {
	// AccountRef selects the CloudflareAccount whose token is used.
	AccountRef AccountRef `json:"accountRef"`
	// Zone the record lives in.
	Zone ZoneRef `json:"zone"`
	// Type of the record.
	// +kubebuilder:validation:Enum=A;AAAA;CNAME;TXT;MX;NS;SRV;CAA;PTR
	Type string `json:"type"`
	// Name of the record, e.g. "app.example.com" or "@" for the zone apex.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Content of the record (IP, hostname, text...).
	// +kubebuilder:validation:MinLength=1
	Content string `json:"content"`
	// TTL in seconds. 1 means "automatic". Proxied records always use automatic TTL.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	TTL int `json:"ttl,omitempty"`
	// Proxied routes traffic through Cloudflare (only A, AAAA and CNAME).
	// +optional
	Proxied bool `json:"proxied,omitempty"`
	// Priority for MX and SRV records.
	// +optional
	Priority *int `json:"priority,omitempty"`
	// Comment shown in the dashboard.
	// +optional
	Comment string `json:"comment,omitempty"`
	// Tags attached to the record (requires a plan that supports record tags).
	// +optional
	Tags []string `json:"tags,omitempty"`
	// DeletionPolicy controls whether the record is removed from Cloudflare on delete.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// DNSRecordStatus is the observed state of a DNSRecord.
type DNSRecordStatus struct {
	ResourceStatus `json:",inline"`
	// RecordID on Cloudflare.
	// +optional
	RecordID string `json:"recordID,omitempty"`
	// ZoneID the record was created in.
	// +optional
	ZoneID string `json:"zoneID,omitempty"`
	// FQDN as reported by Cloudflare.
	// +optional
	FQDN string `json:"fqdn,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cfdns
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Content",type=string,JSONPath=`.spec.content`
// +kubebuilder:printcolumn:name="Proxied",type=boolean,JSONPath=`.spec.proxied`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DNSRecord manages one DNS record in a Cloudflare zone.
type DNSRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DNSRecordSpec   `json:"spec,omitempty"`
	Status DNSRecordStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DNSRecordList contains a list of DNSRecord.
type DNSRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DNSRecord `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DNSRecord{}, &DNSRecordList{})
}

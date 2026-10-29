package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Finalizer is added to every resource that owns something on Cloudflare.
	Finalizer = Group + "/finalizer"

	// LabelManagedBy is set on every Kubernetes object the operator creates.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByValue is the value of LabelManagedBy.
	ManagedByValue = "cloudflare-operator"
	// LabelTunnel is set on objects that belong to a Tunnel.
	LabelTunnel = Group + "/tunnel"
)

// Condition types shared by all resources.
const (
	// ConditionReady is True when the resource exists on Cloudflare and matches the spec.
	ConditionReady = "Ready"
	// ConditionSynced is True when the last reconcile succeeded in talking to Cloudflare.
	ConditionSynced = "Synced"
)

// Condition reasons shared by all resources.
const (
	ReasonReconciling        = "Reconciling"
	ReasonSynced             = "Synced"
	ReasonAccountNotReady    = "AccountNotReady"
	ReasonAccountNotFound    = "AccountNotFound"
	ReasonInvalidSpec        = "InvalidSpec"
	ReasonAPIError           = "CloudflareAPIError"
	ReasonDependencyNotReady = "DependencyNotReady"
	ReasonDeleting           = "Deleting"
	ReasonRetained           = "Retained"
)

// DeletionPolicy controls what happens to the Cloudflare object when the Kubernetes object is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	// DeletionPolicyDelete removes the Cloudflare object when the Kubernetes object is deleted.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyRetain keeps the Cloudflare object and only removes the Kubernetes object.
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// AccountRef points at a cluster-scoped CloudflareAccount.
type AccountRef struct {
	// Name of the CloudflareAccount resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SecretKeyRef selects a key of a Secret, optionally in another namespace.
type SecretKeyRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace of the Secret. Required for cluster-scoped resources.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Key inside the Secret data. Defaults to "apiToken".
	// +kubebuilder:default=apiToken
	// +optional
	Key string `json:"key,omitempty"`
}

// LocalObjectRef references an object in the same namespace.
type LocalObjectRef struct {
	// Name of the referenced object.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ResourceStatus holds the status fields every resource shares.
type ResourceStatus struct {
	// Conditions describe the current state of the resource.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedGeneration is the metadata.generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// GetConditions implements the conditions accessor used by helpers.
func (s *ResourceStatus) GetConditions() []metav1.Condition { return s.Conditions }

// SetConditions implements the conditions accessor used by helpers.
func (s *ResourceStatus) SetConditions(c []metav1.Condition) { s.Conditions = c }

// SetObservedGeneration records the generation the status reflects.
func (s *ResourceStatus) SetObservedGeneration(g int64) { s.ObservedGeneration = g }

// Package v1alpha1 contains API Schema definitions for the cloudflare v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=cloudflare.miho-labs.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	// Group is the API group of every resource in this package.
	Group = "cloudflare.miho-labs.dev"
	// Version is the API version of every resource in this package.
	Version = "v1alpha1"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

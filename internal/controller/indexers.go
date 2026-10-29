package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
)

// TunnelRefIndexer extracts spec.tunnelRef.name of a TunnelIngress for the field index.
func TunnelRefIndexer(obj client.Object) []string {
	ti, ok := obj.(*v1alpha1.TunnelIngress)
	if !ok {
		return nil
	}
	return []string{ti.Spec.TunnelRef.Name}
}

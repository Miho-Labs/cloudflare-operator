// Package cloudflared builds the Kubernetes objects that run cloudflared for a Tunnel.
package cloudflared

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
)

const (
	// DefaultImage is used when neither the Tunnel nor the operator flag sets one.
	DefaultImage = "cloudflare/cloudflared:2025.10.0"
	// TokenKey is the Secret key holding the connector token.
	TokenKey = "TUNNEL_TOKEN"
	// MetricsPort is where cloudflared exposes /ready and /metrics.
	MetricsPort = 2000
)

// SecretName returns the name of the token Secret for a Tunnel.
func SecretName(tunnel *v1alpha1.Tunnel) string { return tunnel.Name + "-cloudflared" }

// DeploymentName returns the name of the cloudflared Deployment for a Tunnel.
func DeploymentName(tunnel *v1alpha1.Tunnel) string { return tunnel.Name + "-cloudflared" }

// Labels returns the selector labels for a Tunnel's pods.
func Labels(tunnel *v1alpha1.Tunnel) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "cloudflared",
		"app.kubernetes.io/instance":  tunnel.Name,
		"app.kubernetes.io/component": "tunnel-connector",
		v1alpha1.LabelManagedBy:       v1alpha1.ManagedByValue,
		v1alpha1.LabelTunnel:          tunnel.Name,
	}
}

// Enabled reports whether the operator should run cloudflared for the Tunnel.
func Enabled(tunnel *v1alpha1.Tunnel) bool {
	return tunnel.Spec.Cloudflared.Enabled == nil || *tunnel.Spec.Cloudflared.Enabled
}

// Args returns the cloudflared command line for a remotely-managed tunnel.
func Args(tunnel *v1alpha1.Tunnel) []string {
	protocol := tunnel.Spec.Cloudflared.Protocol
	if protocol == "" {
		protocol = "auto"
	}
	args := []string{
		"tunnel",
		"--no-autoupdate",
		"--metrics", fmt.Sprintf("0.0.0.0:%d", MetricsPort),
		"--protocol", protocol,
	}
	args = append(args, tunnel.Spec.Cloudflared.ExtraArgs...)
	return append(args, "run")
}

// MutateDeployment fills dep with the desired cloudflared Deployment for the Tunnel. It is meant for
// controllerutil.CreateOrUpdate, so it only sets the fields the operator owns.
func MutateDeployment(dep *appsv1.Deployment, tunnel *v1alpha1.Tunnel, secretName, defaultImage string) {
	spec := tunnel.Spec.Cloudflared
	labels := Labels(tunnel)
	image := spec.Image
	if image == "" {
		image = defaultImage
	}
	if image == "" {
		image = DefaultImage
	}
	replicas := int32(2)
	if spec.Replicas != nil {
		replicas = *spec.Replicas
	}
	resources := spec.Resources
	if resources.Requests == nil && resources.Limits == nil {
		resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
		}
	}
	affinity := spec.Affinity
	if affinity == nil {
		affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{v1alpha1.LabelTunnel: tunnel.Name}},
					TopologyKey:   "kubernetes.io/hostname",
				},
			}},
		}}
	}
	podAnnotations := map[string]string{}
	for k, v := range spec.PodAnnotations {
		podAnnotations[k] = v
	}

	if dep.Labels == nil {
		dep.Labels = map[string]string{}
	}
	for k, v := range labels {
		dep.Labels[k] = v
	}
	dep.Spec.Replicas = &replicas
	dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{v1alpha1.LabelTunnel: tunnel.Name, "app.kubernetes.io/name": "cloudflared"}}
	dep.Spec.Strategy = appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: ptr(intstr.FromInt32(0)),
			MaxSurge:       ptr(intstr.FromInt32(1)),
		},
	}
	dep.Spec.Template.Labels = labels
	dep.Spec.Template.Annotations = podAnnotations
	dep.Spec.Template.Spec = corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "cloudflared",
			Image: image,
			Args:  Args(tunnel),
			Env: []corev1.EnvVar{{
				Name: TokenKey,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  TokenKey,
				}},
			}},
			Ports: []corev1.ContainerPort{{Name: "metrics", ContainerPort: MetricsPort, Protocol: corev1.ProtocolTCP}},
			LivenessProbe: &corev1.Probe{
				ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("metrics")}},
				InitialDelaySeconds: 10,
				PeriodSeconds:       10,
				FailureThreshold:    3,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("metrics")}},
				PeriodSeconds:    5,
				FailureThreshold: 3,
			},
			Resources: resources,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr(false),
				ReadOnlyRootFilesystem:   ptr(true),
				RunAsNonRoot:             ptr(true),
				RunAsUser:                ptr(int64(65532)),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
		SecurityContext:   &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		NodeSelector:      spec.NodeSelector,
		Tolerations:       spec.Tolerations,
		Affinity:          affinity,
		PriorityClassName: spec.PriorityClassName,
	}
}

func ptr[T any](v T) *T { return &v }

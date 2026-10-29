package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflared"
)

// TunnelReconciler manages tunnels, their connector Deployment and their remote configuration.
type TunnelReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
	// CloudflaredImage is the default connector image (operator flag).
	CloudflaredImage string
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnels/finalizers,verbs=update
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=tunnelingresses,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives the tunnel to the desired state.
func (r *TunnelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tunnel := &v1alpha1.Tunnel{}
	if err := r.Get(ctx, req.NamespacedName, tunnel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !tunnel.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, tunnel)
	}
	if !controllerutil.ContainsFinalizer(tunnel, v1alpha1.Finalizer) {
		orig := tunnel.DeepCopy()
		controllerutil.AddFinalizer(tunnel, v1alpha1.Finalizer)
		if err := r.Patch(ctx, tunnel, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := tunnel.DeepCopy()
	res, err := finish(tunnel, &tunnel.Status, r.reconcile(ctx, tunnel), r.resync())
	if perr := patchStatus(ctx, r.Client, orig, tunnel); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *TunnelReconciler) resync() time.Duration {
	if r.ResyncPeriod == 0 {
		return DefaultResyncPeriod
	}
	return r.ResyncPeriod
}

func tunnelName(t *v1alpha1.Tunnel) string {
	if t.Spec.Name != "" {
		return t.Spec.Name
	}
	return t.Name
}

func (r *TunnelReconciler) reconcile(ctx context.Context, tunnel *v1alpha1.Tunnel) error {
	cl, _, err := r.Clients.Resolve(ctx, tunnel.Spec.AccountRef)
	if err != nil {
		return err
	}
	remote, err := r.ensureTunnel(ctx, cl, tunnel)
	if err != nil {
		return err
	}
	tunnel.Status.TunnelID = remote.ID
	tunnel.Status.TunnelName = remote.Name
	tunnel.Status.ConnectorStatus = remote.Status

	if err := r.ensureTokenSecret(ctx, cl, tunnel); err != nil {
		return err
	}
	if err := r.ensureDeployment(ctx, tunnel); err != nil {
		return err
	}
	if err := r.ensureConfig(ctx, cl, tunnel); err != nil {
		return err
	}
	setReady(tunnel, &tunnel.Status, v1alpha1.ReasonSynced, fmt.Sprintf("tunnel %s (%s) is in sync with %d ingress rule(s)", remote.Name, remote.ID, tunnel.Status.IngressCount))
	return nil
}

func (r *TunnelReconciler) ensureTunnel(ctx context.Context, cl *cloudflare.Client, tunnel *v1alpha1.Tunnel) (*cloudflare.Tunnel, error) {
	name := tunnelName(tunnel)
	if tunnel.Status.TunnelID != "" {
		remote, err := cl.GetTunnel(ctx, tunnel.Status.TunnelID)
		if err == nil {
			return remote, nil
		}
		if !cloudflare.IsNotFound(err) {
			return nil, err
		}
		r.Recorder.Eventf(tunnel, corev1.EventTypeWarning, EventFailed, "tunnel %s disappeared from Cloudflare, recreating", tunnel.Status.TunnelID)
		tunnel.Status.TunnelID = ""
	}
	existing, err := cl.FindTunnelByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if !existing.RemoteConfig {
			return nil, fmt.Errorf("tunnel %q (%s) exists but is locally managed; the operator only manages remotely-configured tunnels", name, existing.ID)
		}
		r.Recorder.Eventf(tunnel, corev1.EventTypeNormal, EventAdopted, "adopted existing tunnel %s (%s)", existing.Name, existing.ID)
		return existing, nil
	}
	created, err := cl.CreateTunnel(ctx, name)
	if err != nil {
		return nil, err
	}
	r.Recorder.Eventf(tunnel, corev1.EventTypeNormal, EventCreated, "created tunnel %s (%s)", created.Name, created.ID)
	return created, nil
}

const annotationTunnelID = v1alpha1.Group + "/tunnel-id"

func (r *TunnelReconciler) ensureTokenSecret(ctx context.Context, cl *cloudflare.Client, tunnel *v1alpha1.Tunnel) error {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: tunnel.Namespace, Name: cloudflared.SecretName(tunnel)}
	err := r.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	upToDate := err == nil && len(secret.Data[cloudflared.TokenKey]) > 0 && secret.Annotations[annotationTunnelID] == tunnel.Status.TunnelID
	if upToDate {
		tunnel.Status.TokenSecretName = secret.Name
		return nil
	}
	token, err := cl.GetTunnelToken(ctx, tunnel.Status.TunnelID)
	if err != nil {
		return err
	}
	secret = &corev1.Secret{}
	secret.Name, secret.Namespace = key.Name, key.Namespace
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = managedLabels(map[string]string{v1alpha1.LabelTunnel: tunnel.Name})
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[annotationTunnelID] = tunnel.Status.TunnelID
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			cloudflared.TokenKey: []byte(token),
			"TUNNEL_ID":          []byte(tunnel.Status.TunnelID),
		}
		return controllerutil.SetControllerReference(tunnel, secret, r.Scheme())
	})
	if err != nil {
		return fmt.Errorf("write token secret: %w", err)
	}
	tunnel.Status.TokenSecretName = secret.Name
	return nil
}

func (r *TunnelReconciler) ensureDeployment(ctx context.Context, tunnel *v1alpha1.Tunnel) error {
	dep := &appsv1.Deployment{}
	dep.Name, dep.Namespace = cloudflared.DeploymentName(tunnel), tunnel.Namespace
	if !cloudflared.Enabled(tunnel) {
		err := r.Get(ctx, client.ObjectKeyFromObject(dep), dep)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if dep.Labels[v1alpha1.LabelManagedBy] == v1alpha1.ManagedByValue {
			return client.IgnoreNotFound(r.Delete(ctx, dep))
		}
		return nil
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		cloudflared.MutateDeployment(dep, tunnel, cloudflared.SecretName(tunnel), r.CloudflaredImage)
		return controllerutil.SetControllerReference(tunnel, dep, r.Scheme())
	})
	if err != nil {
		return fmt.Errorf("reconcile cloudflared deployment: %w", err)
	}
	if op != controllerutil.OperationResultNone {
		r.Recorder.Eventf(tunnel, corev1.EventTypeNormal, EventUpdated, "cloudflared deployment %s %s", dep.Name, op)
	}
	return nil
}

// ServiceURL resolves the origin URL of a TunnelIngress.
func ServiceURL(ti *v1alpha1.TunnelIngress) string {
	s := ti.Spec.Service
	if s.URL != "" {
		return s.URL
	}
	scheme := s.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d", scheme, s.Name, ti.Namespace, s.Port)
}

// IngressKey is the "hostname[path]" identifier recorded in Tunnel status.
func IngressKey(ti *v1alpha1.TunnelIngress) string {
	return ti.Spec.Hostname + ti.Spec.Path
}

func toOriginRequest(o *v1alpha1.OriginRequest) *cloudflare.OriginRequest {
	if o == nil {
		return nil
	}
	return &cloudflare.OriginRequest{
		NoTLSVerify:            o.NoTLSVerify,
		HTTPHostHeader:         o.HTTPHostHeader,
		OriginServerName:       o.OriginServerName,
		ConnectTimeout:         o.ConnectTimeout,
		TLSTimeout:             o.TLSTimeout,
		TCPKeepAlive:           o.TCPKeepAlive,
		KeepAliveConnections:   o.KeepAliveConnections,
		KeepAliveTimeout:       o.KeepAliveTimeout,
		DisableChunkedEncoding: o.DisableChunkedEncoding,
		HTTP2Origin:            o.HTTP2Origin,
		NoHappyEyeballs:        o.NoHappyEyeballs,
		ProxyType:              o.ProxyType,
		CAPool:                 o.CAPool,
	}
}

// DesiredConfig builds the tunnel configuration from the TunnelIngress objects attached to it.
func DesiredConfig(tunnel *v1alpha1.Tunnel, ingresses []v1alpha1.TunnelIngress) cloudflare.TunnelConfig {
	cfg := cloudflare.TunnelConfig{OriginRequest: toOriginRequest(tunnel.Spec.OriginRequest)}
	for i := range ingresses {
		ti := &ingresses[i]
		if !ti.DeletionTimestamp.IsZero() {
			continue
		}
		cfg.Ingress = append(cfg.Ingress, cloudflare.IngressRule{
			Hostname:      ti.Spec.Hostname,
			Path:          ti.Spec.Path,
			Service:       ServiceURL(ti),
			OriginRequest: toOriginRequest(ti.Spec.OriginRequest),
		})
	}
	cloudflare.SortIngress(cfg.Ingress)
	catchAll := tunnel.Spec.CatchAllService
	if catchAll == "" {
		catchAll = "http_status:404"
	}
	cfg.Ingress = append(cfg.Ingress, cloudflare.IngressRule{Service: catchAll})
	return cfg
}

func (r *TunnelReconciler) ensureConfig(ctx context.Context, cl *cloudflare.Client, tunnel *v1alpha1.Tunnel) error {
	list := &v1alpha1.TunnelIngressList{}
	if err := r.List(ctx, list, client.InNamespace(tunnel.Namespace), client.MatchingFields{IndexTunnelRef: tunnel.Name}); err != nil {
		return fmt.Errorf("list tunnel ingresses: %w", err)
	}
	desired := DesiredConfig(tunnel, list.Items)
	current, err := cl.GetTunnelConfig(ctx, tunnel.Status.TunnelID)
	if err != nil {
		return err
	}
	version := current.Version
	if !current.Equal(desired) {
		version, err = cl.UpdateTunnelConfig(ctx, tunnel.Status.TunnelID, desired)
		if err != nil {
			return err
		}
		r.Recorder.Eventf(tunnel, corev1.EventTypeNormal, EventUpdated, "pushed tunnel configuration version %d with %d ingress rule(s)", version, len(desired.Ingress)-1)
	}
	hostnames := make([]string, 0, len(desired.Ingress))
	for _, rule := range desired.Ingress {
		if rule.Hostname != "" {
			hostnames = append(hostnames, rule.Hostname+rule.Path)
		}
	}
	tunnel.Status.IngressCount = len(hostnames)
	tunnel.Status.SyncedHostnames = hostnames
	tunnel.Status.ConfigVersion = version
	return nil
}

func (r *TunnelReconciler) finalize(ctx context.Context, tunnel *v1alpha1.Tunnel) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(tunnel, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	// The Deployment and Secret are garbage collected through their owner references.
	if deletionPolicy(tunnel.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && tunnel.Status.TunnelID != "" {
		cl, _, err := r.Clients.Resolve(ctx, tunnel.Spec.AccountRef)
		if err != nil {
			return r.finalizeError(ctx, tunnel, err)
		}
		if err := cl.DeleteTunnel(ctx, tunnel.Status.TunnelID); err != nil {
			return r.finalizeError(ctx, tunnel, err)
		}
		r.Recorder.Eventf(tunnel, corev1.EventTypeNormal, EventDeleted, "deleted tunnel %s from Cloudflare", tunnel.Status.TunnelID)
	} else if tunnel.Status.TunnelID != "" {
		r.Recorder.Eventf(tunnel, corev1.EventTypeNormal, EventRetained, "kept tunnel %s on Cloudflare (deletionPolicy=Retain)", tunnel.Status.TunnelID)
	}
	orig := tunnel.DeepCopy()
	controllerutil.RemoveFinalizer(tunnel, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, tunnel, client.MergeFrom(orig)))
}

func (r *TunnelReconciler) finalizeError(ctx context.Context, tunnel *v1alpha1.Tunnel, err error) (ctrl.Result, error) {
	orig := tunnel.DeepCopy()
	res, rerr := finish(tunnel, &tunnel.Status, err, r.resync())
	setNotReady(tunnel, &tunnel.Status, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, r.Client, orig, tunnel)
	return res, rerr
}

// SetupWithManager registers the controller.
func (r *TunnelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Tunnel{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Secret{}).
		Watches(&v1alpha1.TunnelIngress{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
			ti, ok := o.(*v1alpha1.TunnelIngress)
			if !ok {
				return nil
			}
			return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: ti.Namespace, Name: ti.Spec.TunnelRef.Name}}}
		})).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.TunnelList{}, o.GetName())
		})).
		Named("tunnel").
		Complete(r)
}

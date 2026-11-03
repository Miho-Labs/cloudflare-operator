package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// The owasp score threshold is set through an override of the ruleset's single evaluation rule.
const owaspEvaluationRuleID = "6179ae15870a4bb7b2d480d4843b323c"

func resyncOr(d time.Duration) time.Duration {
	if d == 0 {
		return DefaultResyncPeriod
	}
	return d
}

// ---------------------------------------------------------------------------------------------
// RateLimitRule

// RateLimitRuleReconciler manages rate limiting rules.
type RateLimitRuleReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=ratelimitrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=ratelimitrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=ratelimitrules/finalizers,verbs=update

// Reconcile drives the rule to the desired state.
func (r *RateLimitRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &v1alpha1.RateLimitRule{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.DeletionTimestamp.IsZero() {
		return finalizeRulesetOwner(ctx, r.Client, r.Recorder, r.Clients, obj, obj.Spec.AccountRef, &obj.Status, obj.Spec.DeletionPolicy, resyncOr(r.ResyncPeriod))
	}
	if added, err := ensureFinalizer(ctx, r.Client, obj); err != nil || added {
		return ctrl.Result{}, err
	}
	orig := obj.DeepCopy()
	res, err := finish(obj, &obj.Status, r.reconcile(ctx, obj), resyncOr(r.ResyncPeriod))
	if perr := patchStatus(ctx, r.Client, orig, obj); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *RateLimitRuleReconciler) reconcile(ctx context.Context, obj *v1alpha1.RateLimitRule) error {
	cl, _, err := r.Clients.Resolve(ctx, obj.Spec.AccountRef)
	if err != nil {
		return err
	}
	spec := obj.Spec
	action := spec.Action
	if action == "" {
		action = "block"
	}
	rl := spec.RateLimit
	period := rl.Period
	if period == 0 {
		period = 10
	}
	chars := rl.Characteristics
	if len(chars) == 0 {
		chars = []string{"ip.src", "cf.colo.id"}
	}
	timeout := rl.MitigationTimeout
	if timeout == 0 {
		timeout = period
	}
	desired := cloudflare.Rule{
		Description:      spec.Description,
		Expression:       spec.Expression,
		Action:           action,
		Enabled:          spec.Enabled,
		ActionParameters: blockResponseParams(action, spec.BlockResponse),
		Ratelimit: &cloudflare.RatelimitParams{
			Characteristics:    chars,
			Period:             period,
			RequestsPerPeriod:  rl.RequestsPerPeriod,
			MitigationTimeout:  timeout,
			CountingExpression: rl.CountingExpression,
			RequestsToOrigin:   rl.RequestsToOrigin,
		},
	}
	if err := reconcileRulesetRule(ctx, r.Recorder, cl, obj, spec.Zone, &obj.Status, cloudflare.PhaseRateLimit, desired, spec.Position); err != nil {
		return err
	}
	setReady(obj, &obj.Status, v1alpha1.ReasonSynced, fmt.Sprintf("rate limit rule %q is in sync", spec.Description))
	return nil
}

// SetupWithManager registers the controller.
func (r *RateLimitRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.RateLimitRule{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.RateLimitRuleList{}, o.GetName())
		})).
		Named("ratelimitrule").
		Complete(r)
}

// ---------------------------------------------------------------------------------------------
// WAFRule

// WAFRuleReconciler manages custom WAF rules.
type WAFRuleReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=wafrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=wafrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=wafrules/finalizers,verbs=update

// Reconcile drives the rule to the desired state.
func (r *WAFRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &v1alpha1.WAFRule{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.DeletionTimestamp.IsZero() {
		return finalizeRulesetOwner(ctx, r.Client, r.Recorder, r.Clients, obj, obj.Spec.AccountRef, &obj.Status, obj.Spec.DeletionPolicy, resyncOr(r.ResyncPeriod))
	}
	if added, err := ensureFinalizer(ctx, r.Client, obj); err != nil || added {
		return ctrl.Result{}, err
	}
	orig := obj.DeepCopy()
	res, err := finish(obj, &obj.Status, r.reconcile(ctx, obj), resyncOr(r.ResyncPeriod))
	if perr := patchStatus(ctx, r.Client, orig, obj); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *WAFRuleReconciler) reconcile(ctx context.Context, obj *v1alpha1.WAFRule) error {
	cl, _, err := r.Clients.Resolve(ctx, obj.Spec.AccountRef)
	if err != nil {
		return err
	}
	spec := obj.Spec
	action := spec.Action
	if action == "" {
		action = "block"
	}
	params := blockResponseParams(action, spec.BlockResponse)
	if action == "skip" {
		params = map[string]any{}
		if spec.Skip != nil {
			if spec.Skip.Ruleset != "" {
				params["ruleset"] = spec.Skip.Ruleset
			}
			if len(spec.Skip.Phases) > 0 {
				params["phases"] = spec.Skip.Phases
			}
			if len(spec.Skip.Products) > 0 {
				params["products"] = spec.Skip.Products
			}
		}
		if len(params) == 0 {
			params["ruleset"] = "current"
		}
	}
	desired := cloudflare.Rule{
		Description:      spec.Description,
		Expression:       spec.Expression,
		Action:           action,
		Enabled:          spec.Enabled,
		ActionParameters: params,
	}
	if err := reconcileRulesetRule(ctx, r.Recorder, cl, obj, spec.Zone, &obj.Status, cloudflare.PhaseFirewallCustom, desired, spec.Position); err != nil {
		return err
	}
	setReady(obj, &obj.Status, v1alpha1.ReasonSynced, fmt.Sprintf("WAF rule %q is in sync", spec.Description))
	return nil
}

// SetupWithManager registers the controller.
func (r *WAFRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WAFRule{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.WAFRuleList{}, o.GetName())
		})).
		Named("wafrule").
		Complete(r)
}

// ---------------------------------------------------------------------------------------------
// ManagedWAFRule

// ManagedWAFRuleReconciler deploys Cloudflare managed rulesets.
type ManagedWAFRuleReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=managedwafrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=managedwafrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=managedwafrules/finalizers,verbs=update

// Reconcile drives the deployment to the desired state.
func (r *ManagedWAFRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &v1alpha1.ManagedWAFRule{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.DeletionTimestamp.IsZero() {
		return finalizeRulesetOwner(ctx, r.Client, r.Recorder, r.Clients, obj, obj.Spec.AccountRef, &obj.Status.RulesetRuleStatus, obj.Spec.DeletionPolicy, resyncOr(r.ResyncPeriod))
	}
	if added, err := ensureFinalizer(ctx, r.Client, obj); err != nil || added {
		return ctrl.Result{}, err
	}
	orig := obj.DeepCopy()
	res, err := finish(obj, &obj.Status, r.reconcile(ctx, obj), resyncOr(r.ResyncPeriod))
	if perr := patchStatus(ctx, r.Client, orig, obj); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func managedOverrides(spec *v1alpha1.ManagedWAFRuleSpec) map[string]any {
	o := spec.Overrides
	if o == nil {
		return nil
	}
	out := map[string]any{}
	if o.Action != "" {
		out["action"] = o.Action
	}
	if o.Enabled != nil {
		out["enabled"] = *o.Enabled
	}
	var rules []map[string]any
	for _, rr := range o.Rules {
		m := map[string]any{"id": rr.ID}
		if rr.Action != "" {
			m["action"] = rr.Action
		}
		if rr.Enabled != nil {
			m["enabled"] = *rr.Enabled
		}
		if rr.ScoreThreshold != nil {
			m["score_threshold"] = *rr.ScoreThreshold
		}
		rules = append(rules, m)
	}
	if o.ScoreThreshold != nil && spec.Ruleset == "owasp-core" {
		rules = append(rules, map[string]any{"id": owaspEvaluationRuleID, "score_threshold": *o.ScoreThreshold})
	}
	var cats []map[string]any
	for _, c := range o.Categories {
		m := map[string]any{"category": c.Category}
		if c.Action != "" {
			m["action"] = c.Action
		}
		if c.Enabled != nil {
			m["enabled"] = *c.Enabled
		}
		cats = append(cats, m)
	}
	if o.ParanoiaLevel != nil && spec.Ruleset == "owasp-core" {
		// Paranoia level N keeps PL1..N enabled and disables the higher levels.
		for lvl := *o.ParanoiaLevel + 1; lvl <= 4; lvl++ {
			cats = append(cats, map[string]any{"category": fmt.Sprintf("paranoia-level-%d", lvl), "enabled": false})
		}
	}
	if len(rules) > 0 {
		out["rules"] = rules
	}
	if len(cats) > 0 {
		out["categories"] = cats
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *ManagedWAFRuleReconciler) reconcile(ctx context.Context, obj *v1alpha1.ManagedWAFRule) error {
	cl, _, err := r.Clients.Resolve(ctx, obj.Spec.AccountRef)
	if err != nil {
		return err
	}
	spec := obj.Spec
	name, err := managedRulesetName(spec.Ruleset)
	if err != nil {
		return notReady(v1alpha1.ReasonInvalidSpec, err.Error(), 0)
	}
	zoneID, err := resolveZoneRef(ctx, cl, spec.Zone, obj.Status.ZoneID)
	if err != nil {
		return err
	}
	obj.Status.ZoneID = zoneID
	if obj.Status.ManagedRulesetID == "" {
		managed, err := cl.ListManagedRulesets(ctx, zoneID)
		if err != nil {
			return err
		}
		for _, rs := range managed {
			if rs.Name == name {
				obj.Status.ManagedRulesetID = rs.ID
			}
		}
		if obj.Status.ManagedRulesetID == "" {
			return notReady(v1alpha1.ReasonInvalidSpec, fmt.Sprintf("managed ruleset %q is not available on this zone (plan limits?)", name), time.Hour)
		}
	}
	params := map[string]any{"id": obj.Status.ManagedRulesetID}
	if ov := managedOverrides(&spec); ov != nil {
		params["overrides"] = ov
	}
	expr := spec.Expression
	if expr == "" {
		expr = "true"
	}
	desired := cloudflare.Rule{
		Description:      spec.Description,
		Expression:       expr,
		Action:           "execute",
		Enabled:          spec.Enabled,
		ActionParameters: params,
	}
	if err := reconcileRulesetRule(ctx, r.Recorder, cl, obj, spec.Zone, &obj.Status.RulesetRuleStatus, cloudflare.PhaseFirewallManaged, desired, spec.Position); err != nil {
		return err
	}
	setReady(obj, &obj.Status, v1alpha1.ReasonSynced, fmt.Sprintf("%s is deployed", name))
	return nil
}

// SetupWithManager registers the controller.
func (r *ManagedWAFRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ManagedWAFRule{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.ManagedWAFRuleList{}, o.GetName())
		})).
		Named("managedwafrule").
		Complete(r)
}

// ---------------------------------------------------------------------------------------------
// IPAccessRule

// IPAccessRuleReconciler manages IP access rules.
type IPAccessRuleReconciler struct {
	client.Client
	Recorder     record.EventRecorder
	Clients      *ClientFactory
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=ipaccessrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=ipaccessrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cloudflare.miho-labs.dev,resources=ipaccessrules/finalizers,verbs=update

// Reconcile drives the rule to the desired state.
func (r *IPAccessRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &v1alpha1.IPAccessRule{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, obj)
	}
	if added, err := ensureFinalizer(ctx, r.Client, obj); err != nil || added {
		return ctrl.Result{}, err
	}
	orig := obj.DeepCopy()
	res, err := finish(obj, &obj.Status, r.reconcile(ctx, obj), resyncOr(r.ResyncPeriod))
	if perr := patchStatus(ctx, r.Client, orig, obj); perr != nil {
		return ctrl.Result{}, perr
	}
	return res, err
}

func (r *IPAccessRuleReconciler) scope(ctx context.Context, cl *cloudflare.Client, obj *v1alpha1.IPAccessRule) (cloudflare.IPAccessScope, error) {
	if obj.Spec.Scope == "account" {
		return "", nil
	}
	if obj.Spec.Zone == nil {
		return "", notReady(v1alpha1.ReasonInvalidSpec, "spec.zone is required when scope is zone", 0)
	}
	zoneID, err := resolveZoneRef(ctx, cl, *obj.Spec.Zone, obj.Status.ZoneID)
	if err != nil {
		return "", err
	}
	obj.Status.ZoneID = zoneID
	return cloudflare.IPAccessScope(zoneID), nil
}

func (r *IPAccessRuleReconciler) reconcile(ctx context.Context, obj *v1alpha1.IPAccessRule) error {
	cl, _, err := r.Clients.Resolve(ctx, obj.Spec.AccountRef)
	if err != nil {
		return err
	}
	scope, err := r.scope(ctx, cl, obj)
	if err != nil {
		return err
	}
	desired := cloudflare.IPAccessRule{Mode: obj.Spec.Mode, Target: obj.Spec.Target, Value: obj.Spec.Value, Notes: obj.Spec.Notes}

	var current *cloudflare.IPAccessRule
	if obj.Status.RuleID != "" {
		if current, err = cl.GetIPAccessRule(ctx, scope, obj.Status.RuleID); err != nil {
			return err
		}
		if current == nil {
			r.Recorder.Eventf(obj, corev1.EventTypeWarning, EventFailed, "access rule %s disappeared from Cloudflare, recreating", obj.Status.RuleID)
			obj.Status.RuleID = ""
		}
	}
	if current == nil {
		if current, err = cl.FindIPAccessRule(ctx, scope, desired.Target, desired.Value); err != nil {
			return err
		}
		if current != nil {
			r.Recorder.Eventf(obj, corev1.EventTypeNormal, EventAdopted, "adopted existing access rule for %s %s (%s)", current.Target, current.Value, current.ID)
		}
	}
	switch {
	case current == nil:
		if current, err = cl.CreateIPAccessRule(ctx, scope, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(obj, corev1.EventTypeNormal, EventCreated, "created access rule %s %s %s", desired.Mode, desired.Target, desired.Value)
	case current.Target != desired.Target || current.Value != desired.Value:
		// Target/value are immutable on Cloudflare: replace the rule.
		if err := cl.DeleteIPAccessRule(ctx, scope, current.ID); err != nil {
			return err
		}
		if current, err = cl.CreateIPAccessRule(ctx, scope, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(obj, corev1.EventTypeNormal, EventUpdated, "replaced access rule with %s %s %s", desired.Mode, desired.Target, desired.Value)
	case !current.Equal(desired):
		desired.ID = current.ID
		if current, err = cl.UpdateIPAccessRule(ctx, scope, desired); err != nil {
			return err
		}
		r.Recorder.Eventf(obj, corev1.EventTypeNormal, EventUpdated, "updated access rule %s", current.ID)
	}
	obj.Status.RuleID = current.ID
	setReady(obj, &obj.Status, v1alpha1.ReasonSynced, fmt.Sprintf("access rule %s %s %s is in sync", desired.Mode, desired.Target, desired.Value))
	return nil
}

func (r *IPAccessRuleReconciler) finalize(ctx context.Context, obj *v1alpha1.IPAccessRule) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(obj, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if deletionPolicy(obj.Spec.DeletionPolicy) == v1alpha1.DeletionPolicyDelete && obj.Status.RuleID != "" {
		cl, _, err := r.Clients.Resolve(ctx, obj.Spec.AccountRef)
		if err != nil {
			return finalizeFailed(ctx, r.Client, obj, &obj.Status, err, resyncOr(r.ResyncPeriod))
		}
		scope, err := r.scope(ctx, cl, obj)
		if err != nil {
			return finalizeFailed(ctx, r.Client, obj, &obj.Status, err, resyncOr(r.ResyncPeriod))
		}
		if err := cl.DeleteIPAccessRule(ctx, scope, obj.Status.RuleID); err != nil {
			return finalizeFailed(ctx, r.Client, obj, &obj.Status, err, resyncOr(r.ResyncPeriod))
		}
		r.Recorder.Eventf(obj, corev1.EventTypeNormal, EventDeleted, "deleted access rule %s", obj.Status.RuleID)
	} else if obj.Status.RuleID != "" {
		r.Recorder.Eventf(obj, corev1.EventTypeNormal, EventRetained, "kept access rule %s (deletionPolicy=Retain)", obj.Status.RuleID)
	}
	return removeFinalizer(ctx, r.Client, obj)
}

// SetupWithManager registers the controller.
func (r *IPAccessRuleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.IPAccessRule{}).
		Watches(&v1alpha1.CloudflareAccount{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
			return requestsForAccount(ctx, r.Client, &v1alpha1.IPAccessRuleList{}, o.GetName())
		})).
		Named("ipaccessrule").
		Complete(r)
}

// ---------------------------------------------------------------------------------------------
// shared finalizer plumbing for ruleset-rule kinds

// ensureFinalizer adds the finalizer; it returns true when a patch was issued (caller requeues
// through the watch).
func ensureFinalizer(ctx context.Context, c client.Client, obj client.Object) (bool, error) {
	if controllerutil.ContainsFinalizer(obj, v1alpha1.Finalizer) {
		return false, nil
	}
	orig := obj.DeepCopyObject().(client.Object)
	controllerutil.AddFinalizer(obj, v1alpha1.Finalizer)
	return true, c.Patch(ctx, obj, client.MergeFrom(orig))
}

func removeFinalizer(ctx context.Context, c client.Client, obj client.Object) (ctrl.Result, error) {
	orig := obj.DeepCopyObject().(client.Object)
	controllerutil.RemoveFinalizer(obj, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(c.Patch(ctx, obj, client.MergeFrom(orig)))
}

func finalizeFailed(ctx context.Context, c client.Client, obj client.Object, st Conditions, err error, resync time.Duration) (ctrl.Result, error) {
	orig := obj.DeepCopyObject().(client.Object)
	res, rerr := finish(obj, st, err, resync)
	setNotReady(obj, st, v1alpha1.ReasonDeleting, "waiting to delete from Cloudflare: "+err.Error())
	_ = patchStatus(ctx, c, orig, obj)
	return res, rerr
}

// finalizeRulesetOwner runs the finalizer path of a ruleset-rule kind.
func finalizeRulesetOwner(ctx context.Context, c client.Client, rec record.EventRecorder, clients *ClientFactory, obj client.Object,
	accountRef v1alpha1.AccountRef, st *v1alpha1.RulesetRuleStatus, policy v1alpha1.DeletionPolicy, resync time.Duration) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(obj, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if st.RuleID != "" && deletionPolicy(policy) == v1alpha1.DeletionPolicyDelete {
		cl, _, err := clients.Resolve(ctx, accountRef)
		if err != nil {
			return finalizeFailed(ctx, c, obj, st, err, resync)
		}
		if err := finalizeRulesetRule(ctx, rec, cl, obj, st, policy); err != nil {
			return finalizeFailed(ctx, c, obj, st, err, resync)
		}
	} else if st.RuleID != "" {
		rec.Eventf(obj, corev1.EventTypeNormal, EventRetained, "kept rule %s on Cloudflare (deletionPolicy=Retain)", st.RuleID)
	}
	return removeFinalizer(ctx, c, obj)
}

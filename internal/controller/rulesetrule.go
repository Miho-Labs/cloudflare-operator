package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// resolveZoneRef turns a ZoneRef into a zone ID, reusing a previously resolved ID when present.
func resolveZoneRef(ctx context.Context, cl *cloudflare.Client, ref v1alpha1.ZoneRef, cached string) (string, error) {
	if cached != "" {
		return cached, nil
	}
	if ref.ID != "" {
		z, err := cl.ZoneByID(ctx, ref.ID)
		if err != nil {
			return "", err
		}
		return z.ID, nil
	}
	z, err := cl.ZoneByName(ctx, ref.Name)
	if err != nil {
		return "", err
	}
	return z.ID, nil
}

// blockResponseParams converts a BlockResponse into action_parameters.
func blockResponseParams(action string, br *v1alpha1.BlockResponse) map[string]any {
	if action != "block" || br == nil {
		return nil
	}
	resp := map[string]any{"status_code": br.StatusCode}
	ct := br.ContentType
	if ct == "" {
		ct = "text/plain"
	}
	resp["content_type"] = ct
	resp["content"] = br.Body
	return map[string]any{"response": resp}
}

// reconcileRulesetRule drives one rule inside a zone's phase entrypoint ruleset: resolve the zone,
// ensure the entrypoint exists, observe the rule by ID, adopt by description, then add or update.
func reconcileRulesetRule(ctx context.Context, rec record.EventRecorder, cl *cloudflare.Client, obj client.Object,
	zone v1alpha1.ZoneRef, st *v1alpha1.RulesetRuleStatus, phase string, desired cloudflare.Rule, pos *v1alpha1.RulePosition) error {
	zoneID, err := resolveZoneRef(ctx, cl, zone, st.ZoneID)
	if err != nil {
		return err
	}
	st.ZoneID = zoneID
	rs, err := cl.EnsurePhaseEntrypoint(ctx, zoneID, phase)
	if err != nil {
		return err
	}
	st.RulesetID = rs.ID

	var current *cloudflare.Rule
	if st.RuleID != "" {
		if current = rs.FindRuleByID(st.RuleID); current == nil {
			rec.Eventf(obj, corev1.EventTypeWarning, EventFailed, "rule %s disappeared from ruleset %s, recreating", st.RuleID, rs.ID)
			st.RuleID = ""
		}
	}
	if current == nil {
		if current = rs.FindRuleByDescription(desired.Description); current != nil {
			rec.Eventf(obj, corev1.EventTypeNormal, EventAdopted, "adopted existing rule %q (%s)", current.Description, current.ID)
		}
	}

	var position *cloudflare.RulePosition
	if pos != nil && pos.Index != nil {
		position = &cloudflare.RulePosition{Index: *pos.Index}
	}
	switch {
	case current == nil:
		added, err := cl.AddRule(ctx, zoneID, rs.ID, desired, position)
		if err != nil {
			return err
		}
		current = added
		rec.Eventf(obj, corev1.EventTypeNormal, EventCreated, "created rule %q (%s) in %s ruleset", desired.Description, added.ID, phase)
	default:
		moved := position != nil && rs.RuleIndex(current.ID) != position.Index
		if !current.Equal(desired) || moved {
			updated, err := cl.UpdateRule(ctx, zoneID, rs.ID, current.ID, desired, position)
			if err != nil {
				return err
			}
			current = updated
			rec.Eventf(obj, corev1.EventTypeNormal, EventUpdated, "updated rule %q (%s)", desired.Description, current.ID)
		}
	}
	st.RuleID = current.ID
	st.RuleVersion = current.Version
	return nil
}

// finalizeRulesetRule deletes the rule from its ruleset when the policy says so.
func finalizeRulesetRule(ctx context.Context, rec record.EventRecorder, cl *cloudflare.Client, obj client.Object,
	st *v1alpha1.RulesetRuleStatus, policy v1alpha1.DeletionPolicy) error {
	if st.RuleID == "" || st.RulesetID == "" || st.ZoneID == "" {
		return nil
	}
	if deletionPolicy(policy) != v1alpha1.DeletionPolicyDelete {
		rec.Eventf(obj, corev1.EventTypeNormal, EventRetained, "kept rule %s on Cloudflare (deletionPolicy=Retain)", st.RuleID)
		return nil
	}
	if err := cl.DeleteRule(ctx, st.ZoneID, st.RulesetID, st.RuleID); err != nil {
		return err
	}
	rec.Eventf(obj, corev1.EventTypeNormal, EventDeleted, "deleted rule %s from Cloudflare", st.RuleID)
	return nil
}

// managedRulesetName maps the CRD enum to the Cloudflare ruleset name.
func managedRulesetName(key string) (string, error) {
	switch key {
	case "cloudflare-managed":
		return cloudflare.ManagedRulesetCloudflare, nil
	case "owasp-core":
		return cloudflare.ManagedRulesetOWASP, nil
	case "cloudflare-exposed-credentials-check":
		return cloudflare.ManagedRulesetCredentials, nil
	case "cloudflare-free-managed":
		return cloudflare.ManagedRulesetFree, nil
	}
	return "", fmt.Errorf("unknown managed ruleset %q", key)
}

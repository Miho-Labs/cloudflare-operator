package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/miho-labs/cloudflare-operator/api/v1alpha1"
	"github.com/miho-labs/cloudflare-operator/internal/cloudflare"
)

// ruleResolver turns CR rule lists into Cloudflare rules, resolving AccessGroup and
// AccessServiceToken references by name within a namespace.
type ruleResolver struct {
	client.Client
	namespace string
}

func (rr *ruleResolver) groupID(ctx context.Context, ref v1alpha1.AccessRuleRef) (string, error) {
	if ref.ID != "" {
		return ref.ID, nil
	}
	g := &v1alpha1.AccessGroup{}
	if err := rr.Get(ctx, types.NamespacedName{Namespace: rr.namespace, Name: ref.Name}, g); err != nil {
		if apierrors.IsNotFound(err) {
			return "", notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("AccessGroup %q not found", ref.Name), requeueDependency)
		}
		return "", err
	}
	if g.Status.GroupID == "" {
		return "", notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("AccessGroup %q is not ready yet", ref.Name), requeueDependency)
	}
	return g.Status.GroupID, nil
}

func (rr *ruleResolver) serviceTokenID(ctx context.Context, ref v1alpha1.AccessRuleRef) (string, error) {
	if ref.ID != "" {
		return ref.ID, nil
	}
	t := &v1alpha1.AccessServiceToken{}
	if err := rr.Get(ctx, types.NamespacedName{Namespace: rr.namespace, Name: ref.Name}, t); err != nil {
		if apierrors.IsNotFound(err) {
			return "", notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("AccessServiceToken %q not found", ref.Name), requeueDependency)
		}
		return "", err
	}
	if t.Status.TokenID == "" {
		return "", notReady(v1alpha1.ReasonDependencyNotReady, fmt.Sprintf("AccessServiceToken %q is not ready yet", ref.Name), requeueDependency)
	}
	return t.Status.TokenID, nil
}

// rules expands a list of CR rules into Cloudflare rules. Every populated field of every CR rule
// becomes one Cloudflare rule; the result is empty (not nil) when the input is empty so JSON
// serialises as [] like the API expects.
func (rr *ruleResolver) rules(ctx context.Context, in []v1alpha1.AccessRule) ([]cloudflare.AccessRule, error) {
	out := []cloudflare.AccessRule{}
	for _, r := range in {
		for _, e := range r.Emails {
			out = append(out, cloudflare.RuleEmail(e))
		}
		for _, d := range r.EmailDomains {
			out = append(out, cloudflare.RuleEmailDomain(d))
		}
		if r.Everyone {
			out = append(out, cloudflare.RuleEveryone())
		}
		for _, ip := range r.IPs {
			out = append(out, cloudflare.RuleIP(ip))
		}
		for _, c := range r.Countries {
			out = append(out, cloudflare.RuleCountry(c))
		}
		for _, m := range r.LoginMethods {
			out = append(out, cloudflare.RuleLoginMethod(m))
		}
		for _, g := range r.Groups {
			id, err := rr.groupID(ctx, g)
			if err != nil {
				return nil, err
			}
			out = append(out, cloudflare.RuleGroup(id))
		}
		for _, t := range r.ServiceTokens {
			id, err := rr.serviceTokenID(ctx, t)
			if err != nil {
				return nil, err
			}
			out = append(out, cloudflare.RuleServiceToken(id))
		}
		if r.AnyValidServiceToken {
			out = append(out, cloudflare.RuleAnyValidServiceToken())
		}
		for _, gh := range r.GitHubOrganizations {
			if len(gh.Teams) == 0 {
				out = append(out, cloudflare.RuleGitHubOrganization(gh.IdentityProviderID, gh.Name, ""))
			}
			for _, team := range gh.Teams {
				out = append(out, cloudflare.RuleGitHubOrganization(gh.IdentityProviderID, gh.Name, team))
			}
		}
	}
	return out, nil
}

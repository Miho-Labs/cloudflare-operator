package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/shared"
)

// Permission-group names used for R2 tokens, as listed by the permission groups endpoint.
const (
	PermR2BucketItemRead  = "Workers R2 Storage Bucket Item Read"
	PermR2BucketItemWrite = "Workers R2 Storage Bucket Item Write"
	PermR2StorageWrite    = "Workers R2 Storage Write"
)

// PermissionGroupIDs resolves permission-group names to IDs.
func (c *Client) PermissionGroupIDs(ctx context.Context, names ...string) (map[string]string, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	out := map[string]string{}
	iter := c.api.Accounts.Tokens.PermissionGroups.ListAutoPaging(ctx, accounts.TokenPermissionGroupListParams{AccountID: cf.F(c.AccountID)})
	for iter.Next() {
		pg := iter.Current()
		if want[strings.ToLower(pg.Name)] {
			out[pg.Name] = pg.ID
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list token permission groups: %w", err)
	}
	for _, n := range names {
		if _, ok := out[n]; !ok {
			return nil, fmt.Errorf("permission group %q not found (does the operator token have 'Account API Tokens: Edit'?)", n)
		}
	}
	return out, nil
}

// TokenPolicy is one allow policy of an account-owned API token.
type TokenPolicy struct {
	PermissionGroupIDs []string
	// Resources maps resource identifiers (e.g. com.cloudflare.edge.r2.bucket.<acct>_default_<bucket>) to "*".
	Resources map[string]string
}

// CreatedToken is a freshly created token; Value is only ever returned once.
type CreatedToken struct {
	ID    string
	Value string
}

// CreateAccountToken creates an account-owned API token.
func (c *Client) CreateAccountToken(ctx context.Context, name string, policies []TokenPolicy) (*CreatedToken, error) {
	ps := make([]shared.TokenPolicyParam, 0, len(policies))
	for _, p := range policies {
		groups := make([]shared.TokenPolicyPermissionGroupParam, 0, len(p.PermissionGroupIDs))
		for _, id := range p.PermissionGroupIDs {
			groups = append(groups, shared.TokenPolicyPermissionGroupParam{ID: cf.F(id)})
		}
		ps = append(ps, shared.TokenPolicyParam{
			Effect:           cf.F(shared.TokenPolicyEffectAllow),
			PermissionGroups: cf.F(groups),
			Resources:        cf.F[shared.TokenPolicyResourcesUnionParam](shared.TokenPolicyResourcesIAMResourcesTypeObjectStringParam(p.Resources)),
		})
	}
	res, err := c.api.Accounts.Tokens.New(ctx, accounts.TokenNewParams{
		AccountID: cf.F(c.AccountID),
		Name:      cf.F(name),
		Policies:  cf.F(ps),
	})
	if err != nil {
		return nil, fmt.Errorf("create api token %q: %w", name, err)
	}
	return &CreatedToken{ID: res.ID, Value: res.Value}, nil
}

// AccountTokenExists reports whether the token still exists.
func (c *Client) AccountTokenExists(ctx context.Context, id string) (bool, error) {
	_, err := c.api.Accounts.Tokens.Get(ctx, id, accounts.TokenGetParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get api token %s: %w", id, err)
	}
	return true, nil
}

// DeleteAccountToken deletes an account-owned token. A missing token is not an error.
func (c *Client) DeleteAccountToken(ctx context.Context, id string) error {
	_, err := c.api.Accounts.Tokens.Delete(ctx, id, accounts.TokenDeleteParams{AccountID: cf.F(c.AccountID)})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete api token %s: %w", id, err)
	}
	return nil
}

// R2SecretAccessKey derives the S3 secret access key from an API token value, as documented by
// Cloudflare: the secret is the hex-encoded SHA-256 of the token.
func R2SecretAccessKey(tokenValue string) string {
	sum := sha256.Sum256([]byte(tokenValue))
	return hex.EncodeToString(sum[:])
}

// R2BucketResource is the IAM resource identifier of a bucket.
func (c *Client) R2BucketResource(bucket, jurisdiction string) string {
	if jurisdiction == "" {
		jurisdiction = "default"
	}
	return fmt.Sprintf("com.cloudflare.edge.r2.bucket.%s_%s_%s", c.AccountID, jurisdiction, bucket)
}

// AccountResource is the IAM resource identifier of the whole account.
func (c *Client) AccountResource() string {
	return "com.cloudflare.api.account." + c.AccountID
}

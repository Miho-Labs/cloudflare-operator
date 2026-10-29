// Package cloudflare wraps the official Cloudflare SDK with the small, typed surface the
// controllers need. Every method takes the account from the Client so controllers never deal
// with param.Field plumbing.
package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/accounts"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/user"
)

// Client is a Cloudflare API client bound to one account.
type Client struct {
	api       *cf.Client
	AccountID string

	zoneCache sync.Map // zone name -> zoneInfo
}

// Options configure a Client.
type Options struct {
	// APIToken used for every request.
	APIToken string
	// AccountID the client operates on.
	AccountID string
	// BaseURL overrides the API endpoint (tests point this at the fake server).
	BaseURL string
	// HTTPClient overrides the transport.
	HTTPClient *http.Client
	// UserAgent sent with every request.
	UserAgent string
}

// New builds a Client.
func New(o Options) *Client {
	opts := []option.RequestOption{
		option.WithAPIToken(o.APIToken),
		option.WithMaxRetries(3),
	}
	if o.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(o.BaseURL))
	}
	if o.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(o.HTTPClient))
	} else {
		opts = append(opts, option.WithHTTPClient(&http.Client{Timeout: 60 * time.Second}))
	}
	if o.UserAgent != "" {
		opts = append(opts, option.WithHeader("User-Agent", o.UserAgent))
	}
	c := cf.NewClient(opts...)
	return &Client{api: c, AccountID: o.AccountID}
}

// CredentialsHash returns a stable identifier for a token/account pair, used to cache clients
// without keeping the token in memory as a map key.
func CredentialsHash(token, accountID, baseURL string) string {
	sum := sha256.Sum256([]byte(token + "\x00" + accountID + "\x00" + baseURL))
	return hex.EncodeToString(sum[:8])
}

// AccountInfo is what the operator records about a verified account.
type AccountInfo struct {
	ID   string
	Name string
}

// VerifyAccount checks the token can read the account and returns its name.
func (c *Client) VerifyAccount(ctx context.Context) (*AccountInfo, error) {
	acc, err := c.api.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F(c.AccountID)})
	if err != nil {
		return nil, fmt.Errorf("get account %s: %w", c.AccountID, err)
	}
	return &AccountInfo{ID: acc.ID, Name: acc.Name}, nil
}

// TokenInfo is what the token verify endpoint reports.
type TokenInfo struct {
	ID        string
	Status    string
	ExpiresOn time.Time
}

// VerifyToken reports the status of the token in use. It is best-effort: account-owned tokens
// cannot always be verified through the user endpoint.
func (c *Client) VerifyToken(ctx context.Context) (*TokenInfo, error) {
	res, err := c.api.User.Tokens.Verify(ctx)
	if err != nil {
		return nil, err
	}
	return &TokenInfo{ID: res.ID, Status: string(res.Status), ExpiresOn: res.ExpiresOn}, nil
}

// Raw exposes the underlying SDK client for callers that need an endpoint this wrapper does not
// cover yet. Prefer adding a typed method here.
func (c *Client) Raw() *cf.Client { return c.api }

var _ = user.TokenVerifyResponse{}

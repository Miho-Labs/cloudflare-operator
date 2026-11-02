package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

// ZoneInfo identifies a zone.
type ZoneInfo struct {
	ID   string
	Name string
}

type zoneCacheEntry struct {
	info    ZoneInfo
	expires time.Time
}

const zoneCacheTTL = 10 * time.Minute

// ZoneByName resolves a zone name to its ID within the client's account.
func (c *Client) ZoneByName(ctx context.Context, name string) (*ZoneInfo, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if v, ok := c.zoneCache.Load(name); ok {
		if e := v.(zoneCacheEntry); time.Now().Before(e.expires) {
			info := e.info
			return &info, nil
		}
	}
	// The list is filtered by name only; the account is checked on the result so a zone that lives
	// in another account produces a precise error instead of "not found".
	page, err := c.api.Zones.List(ctx, zones.ZoneListParams{Name: cf.F(name)})
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	for _, z := range page.Result {
		if !strings.EqualFold(z.Name, name) {
			continue
		}
		if z.Account.ID != "" && !strings.EqualFold(z.Account.ID, c.AccountID) {
			return nil, &ZoneNotFoundError{Zone: name, AccountID: c.AccountID, Message: fmt.Sprintf("zone %q belongs to account %s, not %s", name, z.Account.ID, c.AccountID)}
		}
		info := ZoneInfo{ID: z.ID, Name: z.Name}
		c.zoneCache.Store(name, zoneCacheEntry{info: info, expires: time.Now().Add(zoneCacheTTL)})
		return &info, nil
	}
	return nil, &ZoneNotFoundError{Zone: name, AccountID: c.AccountID, Message: fmt.Sprintf("zone %q not visible to the token in account %s (does the zone exist and does the token have Zone:Read on it?)", name, c.AccountID)}
}

// ZoneNotFoundError is returned when a zone name cannot be resolved.
type ZoneNotFoundError struct {
	Zone      string
	AccountID string
	Message   string
}

func (e *ZoneNotFoundError) Error() string { return e.Message }

// IsZoneNotFound reports whether err is a zone resolution failure (as opposed to an API error).
func IsZoneNotFound(err error) bool {
	var z *ZoneNotFoundError
	return errors.As(err, &z)
}

// ZoneByID fetches a zone by ID.
func (c *Client) ZoneByID(ctx context.Context, id string) (*ZoneInfo, error) {
	z, err := c.api.Zones.Get(ctx, zones.ZoneGetParams{ZoneID: cf.F(id)})
	if err != nil {
		return nil, fmt.Errorf("get zone %s: %w", id, err)
	}
	return &ZoneInfo{ID: z.ID, Name: z.Name}, nil
}

// ZoneForHostname finds the zone that owns hostname by trying every parent domain, longest first.
func (c *Client) ZoneForHostname(ctx context.Context, hostname string) (*ZoneInfo, error) {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(hostname), "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		if z, err := c.ZoneByName(ctx, candidate); err == nil {
			return z, nil
		} else if !IsZoneNotFound(err) {
			return nil, err
		}
	}
	return nil, &ZoneNotFoundError{Zone: hostname, AccountID: c.AccountID, Message: fmt.Sprintf("no zone visible to the token in account %s matches hostname %q (does the token have Zone:Read on the zone?)", c.AccountID, hostname)}
}

package cloudflare

import (
	"context"
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
	page, err := c.api.Zones.List(ctx, zones.ZoneListParams{
		Name:    cf.F(name),
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(c.AccountID)}),
	})
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	for _, z := range page.Result {
		if strings.EqualFold(z.Name, name) {
			info := ZoneInfo{ID: z.ID, Name: z.Name}
			c.zoneCache.Store(name, zoneCacheEntry{info: info, expires: time.Now().Add(zoneCacheTTL)})
			return &info, nil
		}
	}
	return nil, fmt.Errorf("zone %q not found in account %s", name, c.AccountID)
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
		} else if StatusCode(err) != 0 {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no zone in account %s matches hostname %q", c.AccountID, hostname)
}

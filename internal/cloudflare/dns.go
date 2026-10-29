package cloudflare

import (
	"context"
	"fmt"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/dns"
)

// DNSRecord is the operator's view of a DNS record.
type DNSRecord struct {
	ID       string
	ZoneID   string
	Type     string
	Name     string
	Content  string
	TTL      int
	Proxied  bool
	Priority *int
	Comment  string
	Tags     []string
}

// Equal reports whether the desired spec already matches the remote record.
func (r DNSRecord) Equal(o DNSRecord) bool {
	if !strings.EqualFold(r.Type, o.Type) || !strings.EqualFold(r.Name, o.Name) || r.Proxied != o.Proxied || r.Comment != o.Comment {
		return false
	}
	if !contentEqual(r.Type, r.Content, o.Content) {
		return false
	}
	// Proxied records always report TTL 1.
	if !r.Proxied && r.TTL != o.TTL {
		return false
	}
	if (r.Priority == nil) != (o.Priority == nil) || (r.Priority != nil && *r.Priority != *o.Priority) {
		return false
	}
	return stringSetEqual(r.Tags, o.Tags)
}

func contentEqual(recordType, a, b string) bool {
	switch strings.ToUpper(recordType) {
	case "CNAME", "MX", "NS", "PTR", "SRV":
		return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
	}
	return a == b
}

func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
		if set[s] < 0 {
			return false
		}
	}
	return true
}

func fromRecordResponse(zoneID string, r dns.RecordResponse) DNSRecord {
	rec := DNSRecord{
		ID:      r.ID,
		ZoneID:  zoneID,
		Type:    string(r.Type),
		Name:    r.Name,
		Content: r.Content,
		TTL:     int(r.TTL),
		Proxied: r.Proxied,
		Comment: r.Comment,
	}
	if r.Priority != 0 {
		p := int(r.Priority)
		rec.Priority = &p
	}
	if tags, ok := r.Tags.([]interface{}); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				rec.Tags = append(rec.Tags, s)
			}
		}
	}
	return rec
}

func toRecordBody(r DNSRecord) dns.RecordNewParamsBody {
	body := dns.RecordNewParamsBody{
		Name:    cf.F(r.Name),
		Type:    cf.F(dns.RecordNewParamsBodyType(strings.ToUpper(r.Type))),
		Content: cf.F(r.Content),
		TTL:     cf.F(dns.TTL(r.TTL)),
		Proxied: cf.F(r.Proxied),
		Comment: cf.F(r.Comment),
	}
	if r.Priority != nil {
		body.Priority = cf.F(float64(*r.Priority))
	}
	if r.Tags != nil {
		body.Tags = cf.F[interface{}](r.Tags)
	}
	return body
}

func toRecordUpdateBody(r DNSRecord) dns.RecordUpdateParamsBody {
	body := dns.RecordUpdateParamsBody{
		Name:    cf.F(r.Name),
		Type:    cf.F(dns.RecordUpdateParamsBodyType(strings.ToUpper(r.Type))),
		Content: cf.F(r.Content),
		TTL:     cf.F(dns.TTL(r.TTL)),
		Proxied: cf.F(r.Proxied),
		Comment: cf.F(r.Comment),
	}
	if r.Priority != nil {
		body.Priority = cf.F(float64(*r.Priority))
	}
	if r.Tags != nil {
		body.Tags = cf.F[interface{}](r.Tags)
	}
	return body
}

// GetDNSRecord fetches a record by ID.
func (c *Client) GetDNSRecord(ctx context.Context, zoneID, id string) (*DNSRecord, error) {
	res, err := c.api.DNS.Records.Get(ctx, id, dns.RecordGetParams{ZoneID: cf.F(zoneID)})
	if err != nil {
		return nil, fmt.Errorf("get dns record %s: %w", id, err)
	}
	rec := fromRecordResponse(zoneID, *res)
	return &rec, nil
}

// FindDNSRecord looks a record up by type and exact name. Returns nil when none exists.
func (c *Client) FindDNSRecord(ctx context.Context, zoneID, recordType, name string) (*DNSRecord, error) {
	page, err := c.api.DNS.Records.List(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(name)}),
		Type:   cf.F(dns.RecordListParamsType(strings.ToUpper(recordType))),
	})
	if err != nil {
		return nil, fmt.Errorf("list dns records: %w", err)
	}
	for _, r := range page.Result {
		if strings.EqualFold(r.Name, name) && strings.EqualFold(string(r.Type), recordType) {
			rec := fromRecordResponse(zoneID, r)
			return &rec, nil
		}
	}
	return nil, nil
}

// CreateDNSRecord creates a record.
func (c *Client) CreateDNSRecord(ctx context.Context, r DNSRecord) (*DNSRecord, error) {
	res, err := c.api.DNS.Records.New(ctx, dns.RecordNewParams{ZoneID: cf.F(r.ZoneID), Body: toRecordBody(r)})
	if err != nil {
		return nil, fmt.Errorf("create dns record %s %s: %w", r.Type, r.Name, err)
	}
	rec := fromRecordResponse(r.ZoneID, *res)
	return &rec, nil
}

// UpdateDNSRecord overwrites a record.
func (c *Client) UpdateDNSRecord(ctx context.Context, r DNSRecord) (*DNSRecord, error) {
	res, err := c.api.DNS.Records.Update(ctx, r.ID, dns.RecordUpdateParams{ZoneID: cf.F(r.ZoneID), Body: toRecordUpdateBody(r)})
	if err != nil {
		return nil, fmt.Errorf("update dns record %s: %w", r.ID, err)
	}
	rec := fromRecordResponse(r.ZoneID, *res)
	return &rec, nil
}

// DeleteDNSRecord deletes a record. A missing record is not an error.
func (c *Client) DeleteDNSRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.api.DNS.Records.Delete(ctx, id, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)})
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete dns record %s: %w", id, err)
	}
	return nil
}

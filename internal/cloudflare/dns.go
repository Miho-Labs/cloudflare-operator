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

// Proxiable reports whether a record type can be proxied through Cloudflare. These three types are
// also mutually exclusive at a given name.
func Proxiable(recordType string) bool {
	switch strings.ToUpper(recordType) {
	case "A", "AAAA", "CNAME":
		return true
	}
	return false
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
	}
	if Proxiable(r.Type) {
		body.Proxied = cf.F(r.Proxied)
	}
	if r.Comment != "" {
		body.Comment = cf.F(r.Comment)
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
	}
	if Proxiable(r.Type) {
		body.Proxied = cf.F(r.Proxied)
	}
	// PUT replaces the record, so an empty comment is sent to clear a previous one.
	body.Comment = cf.F(r.Comment)
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

// MaxCommentLength is Cloudflare's limit for DNS record comments.
const MaxCommentLength = 100

// TruncateComment cuts a comment to MaxCommentLength.
func TruncateComment(s string) string {
	if len(s) <= MaxCommentLength {
		return s
	}
	return s[:MaxCommentLength]
}

// IsDuplicateRecord reports Cloudflare error 81058 "An identical record already exists".
func IsDuplicateRecord(err error) bool { return HasCode(err, 81058) }

// FindDNSRecord looks up the record a DNSRecord should adopt: same type, exact name and matching
// content. Names can carry several records of one type (TXT at the apex, MX, NS...), so content is
// part of the identity; without it every object at a name would adopt the same first record.
//
// The A/AAAA/CNAME family gets two extra rules because a CNAME excludes everything else at a name:
// a desired CNAME takes over whatever A/AAAA/CNAME exists there, and a desired A/AAAA takes over an
// existing CNAME. A desired A/AAAA also adopts a lone existing record of its type with different
// content (the usual "manage this address record" intent); when several exist, a new one is
// created next to them. Returns nil when nothing matches.
func (c *Client) FindDNSRecord(ctx context.Context, zoneID, recordType, name, content string) (*DNSRecord, error) {
	recordType = strings.ToUpper(recordType)
	params := dns.RecordListParams{
		ZoneID:  cf.F(zoneID),
		Name:    cf.F(dns.RecordListParamsName{Exact: cf.F(name)}),
		PerPage: cf.F(100.0),
	}
	if !Proxiable(recordType) {
		params.Type = cf.F(dns.RecordListParamsType(recordType))
	}
	page, err := c.api.DNS.Records.List(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list dns records: %w", err)
	}
	var convertible *DNSRecord
	var sameType []DNSRecord
	for _, r := range page.Result {
		if !strings.EqualFold(r.Name, name) {
			continue
		}
		rec := fromRecordResponse(zoneID, r)
		if strings.EqualFold(rec.Type, recordType) {
			if contentEqual(recordType, rec.Content, content) {
				return &rec, nil
			}
			sameType = append(sameType, rec)
			continue
		}
		if convertible == nil && Proxiable(recordType) && Proxiable(rec.Type) && (recordType == "CNAME" || rec.Type == "CNAME") {
			convertible = &rec
		}
	}
	if convertible != nil {
		return convertible, nil
	}
	if Proxiable(recordType) && len(sameType) == 1 {
		return &sameType[0], nil
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

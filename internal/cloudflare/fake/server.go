// Package fake is an in-memory Cloudflare API used by the unit tests. It implements exactly the
// endpoints the operator calls, with the real envelope shape ({success, errors, result, result_info}).
// Add a handler here whenever you add a Cloudflare call to the operator.
package fake

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

// Server is a fake Cloudflare API.
type Server struct {
	*httptest.Server

	mu sync.Mutex

	AccountID   string
	AccountName string
	Token       string

	Zones          map[string]Zone      // id -> zone
	DNSRecords     map[string]DNSRecord // id -> record
	Tunnels        map[string]Tunnel    // id -> tunnel
	TunnelConfigs  map[string]TunnelConfig
	Buckets        map[string]Bucket   // name -> bucket
	APITokens      map[string]APIToken // id -> token
	AccessApps     map[string]map[string]any
	AccessGroups   map[string]map[string]any
	ServiceTokens  map[string]ServiceToken
	NonEmptyBucket map[string]bool // buckets that refuse deletion

	// Calls records "METHOD /path" for every request, in order.
	Calls []string
	// Fail maps "METHOD /path" (path without query) to a status code to return instead of handling.
	Fail map[string]int
}

// Zone is a fake zone.
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DNSRecord is a fake DNS record.
type DNSRecord struct {
	ID       string   `json:"id"`
	ZoneID   string   `json:"zone_id"`
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	TTL      int      `json:"ttl"`
	Proxied  bool     `json:"proxied"`
	Priority *int     `json:"priority,omitempty"`
	Comment  string   `json:"comment"`
	Tags     []string `json:"tags"`
}

// Tunnel is a fake tunnel.
type Tunnel struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	ConfigSrc    string     `json:"config_src"`
	RemoteConfig bool       `json:"remote_config"`
	DeletedAt    *time.Time `json:"deleted_at"`
	Connections  []any      `json:"connections"`
	Token        string     `json:"-"`
}

// TunnelConfig is a fake remote tunnel configuration.
type TunnelConfig struct {
	Config  map[string]any `json:"config"`
	Version int64          `json:"version"`
}

// Bucket is a fake R2 bucket.
type Bucket struct {
	Name         string `json:"name"`
	Location     string `json:"location,omitempty"`
	StorageClass string `json:"storage_class,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
	CreationDate string `json:"creation_date"`
}

// APIToken is a fake account-owned API token.
type APIToken struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	Policies []any  `json:"policies"`
	Status   string `json:"status"`
}

// ServiceToken is a fake Access service token.
type ServiceToken struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	Duration     string    `json:"duration"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// PermissionGroups served by the fake, keyed by name.
var PermissionGroups = map[string]string{
	"Workers R2 Storage Bucket Item Read":  "pg-r2-item-read",
	"Workers R2 Storage Bucket Item Write": "pg-r2-item-write",
	"Workers R2 Storage Write":             "pg-r2-write",
	"Workers R2 Storage Read":              "pg-r2-read",
}

// New starts a fake server. Close it with Close().
func New() *Server {
	s := &Server{
		AccountID:      "0123456789abcdef0123456789abcdef",
		AccountName:    "Fake Account",
		Token:          "fake-token",
		Zones:          map[string]Zone{},
		DNSRecords:     map[string]DNSRecord{},
		Tunnels:        map[string]Tunnel{},
		TunnelConfigs:  map[string]TunnelConfig{},
		Buckets:        map[string]Bucket{},
		APITokens:      map[string]APIToken{},
		AccessApps:     map[string]map[string]any{},
		AccessGroups:   map[string]map[string]any{},
		ServiceTokens:  map[string]ServiceToken{},
		NonEmptyBucket: map[string]bool{},
		Fail:           map[string]int{},
	}
	s.Server = httptest.NewServer(s.routes())
	return s
}

// AddZone registers a zone and returns its ID.
func (s *Server) AddZone(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := newID()
	s.Zones[id] = Zone{ID: id, Name: name}
	return id
}

// CallCount returns how many requests matched "METHOD /path" (prefix match on path).
func (s *Server) CallCount(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.Calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  status < 400,
		"errors":   []apiError{},
		"messages": []any{},
		"result":   result,
	})
}

func writeList(w http.ResponseWriter, result any, count int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  true,
		"errors":   []apiError{},
		"messages": []any{},
		"result":   result,
		"result_info": map[string]any{
			"page": 1, "per_page": 100, "count": count, "total_count": count, "total_pages": 1,
		},
	})
}

func writeError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":  false,
		"errors":   []apiError{{Code: code, Message: msg}},
		"messages": []any{},
		"result":   nil,
	})
}

func notFound(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotFound, 1000, what+" not found")
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	a := "/accounts/{account}"

	mux.HandleFunc("GET "+a, s.getAccount)
	mux.HandleFunc("GET /user/tokens/verify", s.verifyToken)

	mux.HandleFunc("GET /zones", s.listZones)
	mux.HandleFunc("GET /zones/{zone}", s.getZone)
	mux.HandleFunc("GET /zones/{zone}/dns_records", s.listDNS)
	mux.HandleFunc("POST /zones/{zone}/dns_records", s.createDNS)
	mux.HandleFunc("GET /zones/{zone}/dns_records/{id}", s.getDNS)
	mux.HandleFunc("PUT /zones/{zone}/dns_records/{id}", s.updateDNS)
	mux.HandleFunc("DELETE /zones/{zone}/dns_records/{id}", s.deleteDNS)

	mux.HandleFunc("GET "+a+"/cfd_tunnel", s.listTunnels)
	mux.HandleFunc("POST "+a+"/cfd_tunnel", s.createTunnel)
	mux.HandleFunc("GET "+a+"/cfd_tunnel/{id}", s.getTunnel)
	mux.HandleFunc("DELETE "+a+"/cfd_tunnel/{id}", s.deleteTunnel)
	mux.HandleFunc("DELETE "+a+"/cfd_tunnel/{id}/connections", s.deleteTunnelConnections)
	mux.HandleFunc("GET "+a+"/cfd_tunnel/{id}/token", s.getTunnelToken)
	mux.HandleFunc("GET "+a+"/cfd_tunnel/{id}/configurations", s.getTunnelConfig)
	mux.HandleFunc("PUT "+a+"/cfd_tunnel/{id}/configurations", s.putTunnelConfig)

	mux.HandleFunc("GET "+a+"/r2/buckets", s.listBuckets)
	mux.HandleFunc("POST "+a+"/r2/buckets", s.createBucket)
	mux.HandleFunc("GET "+a+"/r2/buckets/{name}", s.getBucket)
	mux.HandleFunc("DELETE "+a+"/r2/buckets/{name}", s.deleteBucket)

	mux.HandleFunc("GET "+a+"/tokens/permission_groups", s.listPermissionGroups)
	mux.HandleFunc("POST "+a+"/tokens", s.createAPIToken)
	mux.HandleFunc("GET "+a+"/tokens/{id}", s.getAPIToken)
	mux.HandleFunc("DELETE "+a+"/tokens/{id}", s.deleteAPIToken)

	mux.HandleFunc("GET "+a+"/access/apps", s.listAccessApps)
	mux.HandleFunc("POST "+a+"/access/apps", s.createAccessApp)
	mux.HandleFunc("GET "+a+"/access/apps/{id}", s.getAccessApp)
	mux.HandleFunc("PUT "+a+"/access/apps/{id}", s.updateAccessApp)
	mux.HandleFunc("DELETE "+a+"/access/apps/{id}", s.deleteAccessApp)

	mux.HandleFunc("GET "+a+"/access/groups", s.listAccessGroups)
	mux.HandleFunc("POST "+a+"/access/groups", s.createAccessGroup)
	mux.HandleFunc("GET "+a+"/access/groups/{id}", s.getAccessGroup)
	mux.HandleFunc("PUT "+a+"/access/groups/{id}", s.updateAccessGroup)
	mux.HandleFunc("DELETE "+a+"/access/groups/{id}", s.deleteAccessGroup)

	mux.HandleFunc("GET "+a+"/access/service_tokens", s.listServiceTokens)
	mux.HandleFunc("POST "+a+"/access/service_tokens", s.createServiceToken)
	mux.HandleFunc("GET "+a+"/access/service_tokens/{id}", s.getServiceToken)
	mux.HandleFunc("DELETE "+a+"/access/service_tokens/{id}", s.deleteServiceToken)
	mux.HandleFunc("POST "+a+"/access/service_tokens/{id}/refresh", s.refreshServiceToken)
	mux.HandleFunc("POST "+a+"/access/service_tokens/{id}/rotate", s.rotateServiceToken)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		s.Calls = append(s.Calls, key)
		status := s.Fail[key]
		s.mu.Unlock()
		if status != 0 {
			writeError(w, status, 9999, "injected failure")
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+s.Token {
			writeError(w, http.StatusUnauthorized, 10000, "Authentication error")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/accounts/") && !strings.HasPrefix(r.URL.Path, "/accounts/"+s.AccountID) {
			writeError(w, http.StatusForbidden, 9109, "Unauthorized to access requested resource")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// --- account ---------------------------------------------------------------

func (s *Server) getAccount(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"id": s.AccountID, "name": s.AccountName, "type": "standard"})
}

func (s *Server) verifyToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"id": "token-id", "status": "active"})
}

// --- zones -----------------------------------------------------------------

func (s *Server) listZones(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.URL.Query().Get("name")
	out := []map[string]any{}
	for _, z := range s.Zones {
		if name == "" || strings.EqualFold(z.Name, name) {
			out = append(out, map[string]any{"id": z.ID, "name": z.Name, "account": map[string]any{"id": s.AccountID}})
		}
	}
	writeList(w, out, len(out))
}

func (s *Server) getZone(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	z, ok := s.Zones[r.PathValue("zone")]
	if !ok {
		notFound(w, "zone")
		return
	}
	writeJSON(w, 200, map[string]any{"id": z.ID, "name": z.Name, "account": map[string]any{"id": s.AccountID}})
}

// --- dns -------------------------------------------------------------------

func (s *Server) listDNS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	zone := r.PathValue("zone")
	q := r.URL.Query()
	out := []DNSRecord{}
	for _, rec := range s.DNSRecords {
		if rec.ZoneID != zone {
			continue
		}
		if n := q.Get("name.exact"); n != "" && !strings.EqualFold(rec.Name, n) {
			continue
		}
		if n := q.Get("name"); n != "" && !strings.EqualFold(rec.Name, n) {
			continue
		}
		if t := q.Get("type"); t != "" && !strings.EqualFold(rec.Type, t) {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeList(w, out, len(out))
}

func (s *Server) createDNS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	zone := r.PathValue("zone")
	if _, ok := s.Zones[zone]; !ok {
		notFound(w, "zone")
		return
	}
	var rec DNSRecord
	if err := decode(r, &rec); err != nil {
		writeError(w, 400, 1001, err.Error())
		return
	}
	for _, existing := range s.DNSRecords {
		if existing.ZoneID == zone && strings.EqualFold(existing.Name, rec.Name) && existing.Type == rec.Type && existing.Type == "CNAME" {
			writeError(w, 400, 81053, "An A, AAAA, or CNAME record with that host already exists.")
			return
		}
	}
	rec.ID = newID()
	rec.ZoneID = zone
	if rec.Proxied {
		rec.TTL = 1
	}
	s.DNSRecords[rec.ID] = rec
	writeJSON(w, 200, rec)
}

func (s *Server) getDNS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.DNSRecords[r.PathValue("id")]
	if !ok || rec.ZoneID != r.PathValue("zone") {
		notFound(w, "record")
		return
	}
	writeJSON(w, 200, rec)
}

func (s *Server) updateDNS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	existing, ok := s.DNSRecords[id]
	if !ok || existing.ZoneID != r.PathValue("zone") {
		notFound(w, "record")
		return
	}
	var rec DNSRecord
	if err := decode(r, &rec); err != nil {
		writeError(w, 400, 1001, err.Error())
		return
	}
	rec.ID = id
	rec.ZoneID = existing.ZoneID
	if rec.Proxied {
		rec.TTL = 1
	}
	s.DNSRecords[id] = rec
	writeJSON(w, 200, rec)
}

func (s *Server) deleteDNS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.DNSRecords[id]; !ok {
		notFound(w, "record")
		return
	}
	delete(s.DNSRecords, id)
	writeJSON(w, 200, map[string]any{"id": id})
}

// --- tunnels ---------------------------------------------------------------

func (s *Server) listTunnels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	out := []Tunnel{}
	for _, t := range s.Tunnels {
		if n := q.Get("name"); n != "" && t.Name != n {
			continue
		}
		if q.Get("is_deleted") == "false" && t.DeletedAt != nil {
			continue
		}
		out = append(out, t)
	}
	writeList(w, out, len(out))
}

func (s *Server) createTunnel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body struct {
		Name         string `json:"name"`
		ConfigSrc    string `json:"config_src"`
		TunnelSecret string `json:"tunnel_secret"`
	}
	if err := decode(r, &body); err != nil || body.Name == "" {
		writeError(w, 400, 1001, "invalid body")
		return
	}
	for _, t := range s.Tunnels {
		if t.Name == body.Name && t.DeletedAt == nil {
			writeError(w, 409, 1013, "tunnel name already exists")
			return
		}
	}
	t := Tunnel{ID: newID(), Name: body.Name, Status: "inactive", ConfigSrc: body.ConfigSrc, RemoteConfig: body.ConfigSrc == "cloudflare", Connections: []any{}, Token: "tok-" + newID()}
	s.Tunnels[t.ID] = t
	writeJSON(w, 200, t)
}

func (s *Server) getTunnel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.Tunnels[r.PathValue("id")]
	if !ok {
		notFound(w, "tunnel")
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) deleteTunnel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	t, ok := s.Tunnels[id]
	if !ok {
		notFound(w, "tunnel")
		return
	}
	now := time.Now()
	t.DeletedAt = &now
	s.Tunnels[id] = t
	writeJSON(w, 200, t)
}

func (s *Server) deleteTunnelConnections(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.Tunnels[r.PathValue("id")]
	if !ok {
		notFound(w, "tunnel")
		return
	}
	t.Connections = []any{}
	s.Tunnels[t.ID] = t
	writeJSON(w, 200, map[string]any{})
}

func (s *Server) getTunnelToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.Tunnels[r.PathValue("id")]
	if !ok {
		notFound(w, "tunnel")
		return
	}
	writeJSON(w, 200, t.Token)
}

func (s *Server) getTunnelConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.Tunnels[id]; !ok {
		notFound(w, "tunnel")
		return
	}
	cfg, ok := s.TunnelConfigs[id]
	if !ok {
		writeJSON(w, 200, map[string]any{"tunnel_id": id, "version": 0, "config": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"tunnel_id": id, "version": cfg.Version, "config": cfg.Config, "source": "cloudflare"})
}

func (s *Server) putTunnelConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.Tunnels[id]; !ok {
		notFound(w, "tunnel")
		return
	}
	var body struct {
		Config map[string]any `json:"config"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, 400, 1001, err.Error())
		return
	}
	ingress, _ := body.Config["ingress"].([]any)
	if len(ingress) == 0 {
		writeError(w, 400, 1002, "at least one ingress rule is required")
		return
	}
	if last, ok := ingress[len(ingress)-1].(map[string]any); !ok || last["hostname"] != nil && last["hostname"] != "" {
		writeError(w, 400, 1002, "the last ingress rule must be a catch-all without hostname")
		return
	}
	cfg := s.TunnelConfigs[id]
	cfg.Version++
	cfg.Config = body.Config
	s.TunnelConfigs[id] = cfg
	writeJSON(w, 200, map[string]any{"tunnel_id": id, "version": cfg.Version, "config": cfg.Config, "source": "cloudflare"})
}

// --- r2 --------------------------------------------------------------------

func (s *Server) listBuckets(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Bucket{}
	for _, b := range s.Buckets {
		out = append(out, b)
	}
	writeJSON(w, 200, map[string]any{"buckets": out})
}

func (s *Server) createBucket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body struct {
		Name         string `json:"name"`
		LocationHint string `json:"locationHint"`
		StorageClass string `json:"storageClass"`
	}
	if err := decode(r, &body); err != nil || body.Name == "" {
		writeError(w, 400, 10001, "invalid body")
		return
	}
	if _, ok := s.Buckets[body.Name]; ok {
		writeError(w, 409, 10004, "The bucket you tried to create already exists, and you own it.")
		return
	}
	b := Bucket{Name: body.Name, Location: body.LocationHint, StorageClass: body.StorageClass, Jurisdiction: r.Header.Get("cf-r2-jurisdiction"), CreationDate: time.Now().UTC().Format(time.RFC3339)}
	if b.Jurisdiction == "" {
		b.Jurisdiction = "default"
	}
	s.Buckets[b.Name] = b
	writeJSON(w, 200, b)
}

func (s *Server) getBucket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.Buckets[r.PathValue("name")]
	if !ok {
		writeError(w, 404, 10006, "The specified bucket does not exist.")
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) deleteBucket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.PathValue("name")
	if _, ok := s.Buckets[name]; !ok {
		writeError(w, 404, 10006, "The specified bucket does not exist.")
		return
	}
	if s.NonEmptyBucket[name] {
		writeError(w, 409, 10008, "The bucket you tried to delete is not empty.")
		return
	}
	delete(s.Buckets, name)
	writeJSON(w, 200, map[string]any{})
}

// --- api tokens ------------------------------------------------------------

func (s *Server) listPermissionGroups(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	names := make([]string, 0, len(PermissionGroups))
	for n := range PermissionGroups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, map[string]any{"id": PermissionGroups[n], "name": n, "scopes": []string{"com.cloudflare.api.account"}})
	}
	writeList(w, out, len(out))
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body struct {
		Name     string `json:"name"`
		Policies []any  `json:"policies"`
	}
	if err := decode(r, &body); err != nil || body.Name == "" || len(body.Policies) == 0 {
		writeError(w, 400, 1001, "invalid body")
		return
	}
	t := APIToken{ID: newID(), Name: body.Name, Value: "v1.0-" + newID() + newID(), Policies: body.Policies, Status: "active"}
	s.APITokens[t.ID] = t
	writeJSON(w, 200, t)
}

func (s *Server) getAPIToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.APITokens[r.PathValue("id")]
	if !ok {
		notFound(w, "token")
		return
	}
	t.Value = ""
	writeJSON(w, 200, t)
}

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.APITokens[id]; !ok {
		notFound(w, "token")
		return
	}
	delete(s.APITokens, id)
	writeJSON(w, 200, map[string]any{"id": id})
}

// --- access apps -----------------------------------------------------------

func (s *Server) listAccessApps(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	domain := r.URL.Query().Get("domain")
	out := []map[string]any{}
	for _, app := range s.AccessApps {
		if domain != "" && app["domain"] != domain {
			continue
		}
		out = append(out, app)
	}
	writeList(w, out, len(out))
}

func (s *Server) createAccessApp(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var app map[string]any
	if err := decode(r, &app); err != nil || app["domain"] == nil {
		writeError(w, 400, 12130, "invalid body")
		return
	}
	for _, existing := range s.AccessApps {
		if existing["domain"] == app["domain"] {
			writeError(w, 400, 12130, "access.api.error.domain_conflict: application with that domain already exists")
			return
		}
	}
	id := newID()
	app["id"] = id
	app["aud"] = newID() + newID()
	assignPolicyIDs(app)
	s.AccessApps[id] = app
	writeJSON(w, 201, app)
}

func assignPolicyIDs(app map[string]any) {
	policies, _ := app["policies"].([]any)
	for _, p := range policies {
		if pm, ok := p.(map[string]any); ok {
			if pm["id"] == nil || pm["id"] == "" {
				pm["id"] = newID()
			}
		}
	}
}

func (s *Server) getAccessApp(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.AccessApps[r.PathValue("id")]
	if !ok {
		notFound(w, "application")
		return
	}
	writeJSON(w, 200, app)
}

func (s *Server) updateAccessApp(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	existing, ok := s.AccessApps[id]
	if !ok {
		notFound(w, "application")
		return
	}
	var app map[string]any
	if err := decode(r, &app); err != nil {
		writeError(w, 400, 12130, err.Error())
		return
	}
	app["id"] = id
	app["aud"] = existing["aud"]
	assignPolicyIDs(app)
	s.AccessApps[id] = app
	writeJSON(w, 200, app)
}

func (s *Server) deleteAccessApp(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.AccessApps[id]; !ok {
		notFound(w, "application")
		return
	}
	delete(s.AccessApps, id)
	writeJSON(w, 200, map[string]any{"id": id})
}

// --- access groups ---------------------------------------------------------

func (s *Server) listAccessGroups(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.URL.Query().Get("name")
	out := []map[string]any{}
	for _, g := range s.AccessGroups {
		if name != "" && g["name"] != name {
			continue
		}
		out = append(out, g)
	}
	writeList(w, out, len(out))
}

func (s *Server) createAccessGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var g map[string]any
	if err := decode(r, &g); err != nil || g["name"] == nil {
		writeError(w, 400, 12130, "invalid body")
		return
	}
	id := newID()
	g["id"] = id
	s.AccessGroups[id] = g
	writeJSON(w, 201, g)
}

func (s *Server) getAccessGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.AccessGroups[r.PathValue("id")]
	if !ok {
		notFound(w, "group")
		return
	}
	writeJSON(w, 200, g)
}

func (s *Server) updateAccessGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.AccessGroups[id]; !ok {
		notFound(w, "group")
		return
	}
	var g map[string]any
	if err := decode(r, &g); err != nil {
		writeError(w, 400, 12130, err.Error())
		return
	}
	g["id"] = id
	s.AccessGroups[id] = g
	writeJSON(w, 200, g)
}

func (s *Server) deleteAccessGroup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.AccessGroups[id]; !ok {
		notFound(w, "group")
		return
	}
	delete(s.AccessGroups, id)
	writeJSON(w, 200, map[string]any{"id": id})
}

// --- service tokens --------------------------------------------------------

func (s *Server) listServiceTokens(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := r.URL.Query().Get("name")
	out := []ServiceToken{}
	for _, t := range s.ServiceTokens {
		if name != "" && t.Name != name {
			continue
		}
		t.ClientSecret = ""
		out = append(out, t)
	}
	writeList(w, out, len(out))
}

func (s *Server) createServiceToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body struct {
		Name     string `json:"name"`
		Duration string `json:"duration"`
	}
	if err := decode(r, &body); err != nil || body.Name == "" {
		writeError(w, 400, 12130, "invalid body")
		return
	}
	if body.Duration == "" {
		body.Duration = "8760h"
	}
	d, err := time.ParseDuration(body.Duration)
	if err != nil {
		writeError(w, 400, 12130, "invalid duration")
		return
	}
	t := ServiceToken{ID: newID(), Name: body.Name, ClientID: newID() + ".access", ClientSecret: newID() + newID(), Duration: body.Duration, ExpiresAt: time.Now().Add(d).UTC().Truncate(time.Second)}
	s.ServiceTokens[t.ID] = t
	writeJSON(w, 201, t)
}

func (s *Server) getServiceToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.ServiceTokens[r.PathValue("id")]
	if !ok {
		notFound(w, "service token")
		return
	}
	t.ClientSecret = ""
	writeJSON(w, 200, t)
}

func (s *Server) deleteServiceToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	t, ok := s.ServiceTokens[id]
	if !ok {
		notFound(w, "service token")
		return
	}
	delete(s.ServiceTokens, id)
	t.ClientSecret = ""
	writeJSON(w, 200, t)
}

func (s *Server) refreshServiceToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.ServiceTokens[r.PathValue("id")]
	if !ok {
		notFound(w, "service token")
		return
	}
	d, _ := time.ParseDuration(t.Duration)
	t.ExpiresAt = time.Now().Add(d).UTC().Truncate(time.Second)
	s.ServiceTokens[t.ID] = t
	t.ClientSecret = ""
	writeJSON(w, 200, t)
}

func (s *Server) rotateServiceToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.ServiceTokens[r.PathValue("id")]
	if !ok {
		notFound(w, "service token")
		return
	}
	t.ClientSecret = newID() + newID()
	s.ServiceTokens[t.ID] = t
	writeJSON(w, 200, t)
}

var _ = fmt.Sprintf

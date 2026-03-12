package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// FlexibleStringSlice unmarshals a JSON value that may be either a []string
// (for groups/lists) or a number (for domains/tenants where Stalwart returns
// a member count). When the value is a number it is silently discarded.
type FlexibleStringSlice []string

func (f *FlexibleStringSlice) UnmarshalJSON(data []byte) error {
	// Try []string first (the common case for groups/lists).
	var ss []string
	if err := json.Unmarshal(data, &ss); err == nil {
		*f = ss
		return nil
	}
	// Stalwart returns a bare integer for domain/tenant member counts — ignore it.
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*f = nil
		return nil
	}
	return fmt.Errorf("members: expected []string or number, got %s", string(data))
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	authHeader string
}

func NewClientBearer(endpoint, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(endpoint, "/"),
		httpClient: &http.Client{},
		authHeader: "Bearer " + token,
	}
}

func NewClientBasic(endpoint, username, password string) *Client {
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return &Client{
		baseURL:    strings.TrimRight(endpoint, "/"),
		httpClient: &http.Client{},
		authHeader: "Basic " + encoded,
	}
}

// Principal represents a Stalwart principal (domain, individual, list, group).
type Principal struct {
	ID                  *int64   `json:"id,omitempty"`
	Type                string   `json:"type"`
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	Quota               int64    `json:"quota,omitempty"`
	Secrets             []string `json:"secrets,omitempty"`
	Emails              []string `json:"emails,omitempty"`
	Roles               []string `json:"roles,omitempty"`
	Members             FlexibleStringSlice `json:"members,omitempty"`
	MemberOf            []string `json:"memberOf,omitempty"`
	Lists               []string `json:"lists,omitempty"`
	ExternalMembers     []string `json:"externalMembers,omitempty"`
	EnabledPermissions  []string `json:"enabledPermissions,omitempty"`
	DisabledPermissions []string `json:"disabledPermissions,omitempty"`
}

// PatchOp is a single update operation for PATCH /principal/{name}.
type PatchOp struct {
	Action string `json:"action"`
	Field  string `json:"field"`
	Value  any    `json:"value"`
}

// DKIMRequest is the body for POST /dkim.
type DKIMRequest struct {
	Domain    string  `json:"domain"`
	Algorithm string  `json:"algorithm"`
	Selector  *string `json:"selector,omitempty"`
}

// DNSRecord is a single DNS record returned by GET /dns/records/{domain}.
type DNSRecord struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// ErrNotFound is returned when the API returns 404.
type ErrNotFound struct{ Name string }

func (e ErrNotFound) Error() string { return fmt.Sprintf("principal %q not found", e.Name) }

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func (c *Client) CreatePrincipal(ctx context.Context, p *Principal) (int64, error) {
	body, status, err := c.do(ctx, "POST", "/principal", p)
	if err != nil {
		return 0, err
	}
	if status >= 400 {
		return 0, fmt.Errorf("create principal: HTTP %d: %s", status, body)
	}
	var resp struct {
		Data int64 `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("create principal: decode response: %w", err)
	}
	return resp.Data, nil
}

func (c *Client) GetPrincipal(ctx context.Context, name string) (*Principal, error) {
	body, status, err := c.do(ctx, "GET", "/principal/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, ErrNotFound{Name: name}
	}
	if status >= 400 {
		return nil, fmt.Errorf("get principal: HTTP %d: %s", status, body)
	}
	var resp struct {
		Data *Principal `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("get principal: decode response: %w", err)
	}
	return resp.Data, nil
}

func (c *Client) UpdatePrincipal(ctx context.Context, name string, ops []PatchOp) error {
	body, status, err := c.do(ctx, "PATCH", "/principal/"+url.PathEscape(name), ops)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("update principal: HTTP %d: %s", status, body)
	}
	return nil
}

func (c *Client) DeletePrincipal(ctx context.Context, name string) error {
	body, status, err := c.do(ctx, "DELETE", "/principal/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	if status == 404 {
		return nil // already gone
	}
	if status >= 400 {
		return fmt.Errorf("delete principal: HTTP %d: %s", status, body)
	}
	return nil
}

// PrincipalList is the paginated response from GET /principal.
type PrincipalList struct {
	Items []Principal `json:"items"`
	Total int         `json:"total"`
}

// ListPrincipals returns all principals of the given types, paging automatically.
func (c *Client) ListPrincipals(ctx context.Context, types ...string) ([]Principal, error) {
	const pageSize = 100
	var all []Principal

	typeParam := strings.Join(types, ",")
	for page := 1; ; page++ {
		path := fmt.Sprintf("/principal?types=%s&page=%d&limit=%d", typeParam, page, pageSize)
		body, status, err := c.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("list principals: HTTP %d: %s", status, body)
		}
		var resp struct {
			Data PrincipalList `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("list principals: decode response: %w", err)
		}
		all = append(all, resp.Data.Items...)
		if len(all) >= resp.Data.Total || len(resp.Data.Items) < pageSize {
			break
		}
	}
	return all, nil
}

// PrincipalsOnDomain returns the names of all principals (accounts and groups)
// that have at least one email address on the given domain.
func (c *Client) PrincipalsOnDomain(ctx context.Context, domain string) ([]string, error) {
	principals, err := c.ListPrincipals(ctx, "individual", "list", "group")
	if err != nil {
		return nil, err
	}
	suffix := "@" + domain
	var found []string
	for _, p := range principals {
		for _, email := range p.Emails {
			if strings.HasSuffix(strings.ToLower(email), strings.ToLower(suffix)) {
				found = append(found, p.Name)
				break
			}
		}
	}
	return found, nil
}

func (c *Client) CreateDKIM(ctx context.Context, req DKIMRequest) ([]DNSRecord, error) {
	body, status, err := c.do(ctx, "POST", "/dkim", req)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("create DKIM: HTTP %d: %s", status, body)
	}
	return c.GetDNSRecords(ctx, req.Domain)
}

func (c *Client) GetDNSRecords(ctx context.Context, domain string) ([]DNSRecord, error) {
	body, status, err := c.do(ctx, "GET", "/dns/records/"+url.PathEscape(domain), nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, ErrNotFound{Name: domain}
	}
	if status >= 400 {
		return nil, fmt.Errorf("get DNS records: HTTP %d: %s", status, body)
	}
	var resp struct {
		Data []DNSRecord `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("get DNS records: decode response: %w", err)
	}
	return resp.Data, nil
}

// ClearSettings removes all settings under a given prefix.
func (c *Client) ClearSettings(ctx context.Context, prefix string) error {
	ops := []map[string]string{{"type": "clear", "prefix": prefix}}
	body, status, err := c.do(ctx, "POST", "/settings", ops)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("clear settings: HTTP %d: %s", status, body)
	}
	return nil
}

// diffStringSlice computes addItem/removeItem ops for unordered array fields
// (roles, members, externalMembers). Only membership changes are emitted.
func diffStringSlice(field string, old, new []string) []PatchOp {
	oldSet := make(map[string]bool, len(old))
	for _, v := range old {
		oldSet[v] = true
	}
	newSet := make(map[string]bool, len(new))
	for _, v := range new {
		newSet[v] = true
	}

	var ops []PatchOp
	for _, v := range new {
		if !oldSet[v] {
			ops = append(ops, PatchOp{Action: "addItem", Field: field, Value: v})
		}
	}
	for _, v := range old {
		if !newSet[v] {
			ops = append(ops, PatchOp{Action: "removeItem", Field: field, Value: v})
		}
	}
	return ops
}

// orderedEmailOps produces ops that fully replace an ordered email list,
// preserving position so that index-0 remains the primary address.
// It removes all old entries first, then adds new ones in order.
func orderedEmailOps(old, new []string) []PatchOp {
	// Fast path: identical slice (same order).
	if slicesEqual(old, new) {
		return nil
	}
	ops := make([]PatchOp, 0, len(old)+len(new))
	for _, v := range old {
		ops = append(ops, PatchOp{Action: "removeItem", Field: "emails", Value: v})
	}
	for _, v := range new {
		ops = append(ops, PatchOp{Action: "addItem", Field: "emails", Value: v})
	}
	return ops
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

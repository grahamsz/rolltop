// File overview: Minimal CardDAV client over raw net/http and encoding/xml.
// Discovery follows RFC 6764 (.well-known/carddav) to the principal's
// addressbook-home-set; incremental sync uses the sync-collection REPORT from
// RFC 6578 and falls back to a full multiget when the server does not support
// it. No third-party WebDAV dependency, to keep the plugin self-contained.

package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const carddavRequestTimeout = 30 * time.Second

// errSyncCollectionUnsupported signals the caller to fall back to a full fetch.
var errSyncCollectionUnsupported = fmt.Errorf("carddav sync-collection is not supported")

// isLoopbackHost reports whether a hostname is local-only, so local test
// servers can run over plain HTTP while remote servers require HTTPS.
func isLoopbackHost(host string) bool {
	lowered := strings.ToLower(strings.TrimSpace(host))
	return lowered == "localhost" || lowered == "127.0.0.1" || lowered == "::1" ||
		strings.HasPrefix(lowered, "127.")
}

// carddavCredentials carries the CardDAV login. The password must never be
// logged; only the username appears in errors.
type carddavCredentials struct {
	Username string
	Password string
}

type carddavClient struct {
	http     *http.Client
	base     *url.URL
	username string
	password string
}

func newCardDAVClient(serverURL string, creds carddavCredentials) (*carddavClient, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("server URL is required")
	}
	base, err := url.Parse(trimmed)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("server URL is invalid")
	}
	if !isLoopbackHost(base.Hostname()) && !strings.EqualFold(base.Scheme, "https") {
		return nil, fmt.Errorf("CardDAV servers must use https, except on loopback hosts")
	}
	if strings.TrimSpace(creds.Username) == "" || creds.Password == "" {
		return nil, fmt.Errorf("username and password are required")
	}
	return &carddavClient{
		http:     &http.Client{Timeout: carddavRequestTimeout},
		base:     base,
		username: strings.TrimSpace(creds.Username),
		password: creds.Password,
	}, nil
}

// addressBook is one discovered CardDAV address book.
type addressBook struct {
	URL         string
	DisplayName string
}

// davMultistatus decodes a DAV multistatus body. Struct tags match local
// element names, so namespace prefixes are ignored by encoding/xml.
type davMultistatus struct {
	XMLName   xml.Name      `xml:"multistatus"`
	Responses []davResponse `xml:"response"`
	SyncToken string        `xml:"sync-token"`
}

type davResponse struct {
	Href      string        `xml:"href"`
	Status    string        `xml:"status"`
	PropStats []davPropstat `xml:"propstat"`
}

type davPropstat struct {
	Prop   davProp `xml:"prop"`
	Status string  `xml:"status"`
}

type davProp struct {
	ETag        string `xml:"getetag"`
	AddressData string `xml:"address-data"`
	DisplayName string `xml:"displayname"`
	// ResourceTypeInner holds the raw resourcetype children; the addressbook
	// marker is detected with strings.Contains to stay namespace-agnostic.
	ResourceTypeInner string `xml:"resourcetype"`
	Principal         struct {
		Href string `xml:"href"`
	} `xml:"current-user-principal"`
	AddressbookHomeSet struct {
		Href string `xml:"href"`
	} `xml:"addressbook-home-set"`
}

type davResult struct {
	body   []byte
	header http.Header
	status int
}

func (c *carddavClient) do(ctx context.Context, method, rawURL, body, contentType string, headers map[string]string) (davResult, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return davResult{}, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("User-Agent", "Rolltop-CardDAV-Sync/1.0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return davResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return davResult{}, err
	}
	return davResult{body: data, header: resp.Header, status: resp.StatusCode}, nil
}

// doFollowRedirect replays a request across 301/302/307/308 redirects while
// preserving the DAV method and body, which net/http would otherwise downgrade
// to GET on some statuses.
func (c *carddavClient) doFollowRedirect(ctx context.Context, method, rawURL, body, contentType string, headers map[string]string) (davResult, error) {
	current := rawURL
	for redirects := 0; redirects < 5; redirects++ {
		res, err := c.do(ctx, method, current, body, contentType, headers)
		if err != nil {
			return davResult{}, err
		}
		if res.status != http.StatusMovedPermanently && res.status != http.StatusFound &&
			res.status != http.StatusTemporaryRedirect && res.status != http.StatusPermanentRedirect {
			return res, nil
		}
		location := strings.TrimSpace(res.header.Get("Location"))
		if location == "" {
			return davResult{}, fmt.Errorf("redirect without Location header")
		}
		next, err := url.Parse(current)
		if err != nil {
			return davResult{}, err
		}
		ref, err := url.Parse(location)
		if err != nil {
			return davResult{}, err
		}
		current = next.ResolveReference(ref).String()
	}
	return davResult{}, fmt.Errorf("too many redirects")
}

func (c *carddavClient) resolve(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return c.base.ResolveReference(ref).String()
}

func parseMultistatus(data []byte) (*davMultistatus, error) {
	var out davMultistatus
	if err := xml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("could not parse DAV response: %w", err)
	}
	return &out, nil
}

func propstatOK(propstats []davPropstat) (davProp, bool) {
	for _, ps := range propstats {
		if strings.Contains(ps.Status, " 200") || strings.TrimSpace(ps.Status) == "" {
			return ps.Prop, true
		}
	}
	return davProp{}, false
}

// DiscoverAddressBooks walks well-known → current-user-principal →
// addressbook-home-set → address books (RFC 6764, RFC 6352 section 7).
func (c *carddavClient) DiscoverAddressBooks(ctx context.Context) ([]addressBook, error) {
	principal, err := c.findCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	homeSet, err := c.findAddressbookHomeSet(ctx, principal)
	if err != nil {
		return nil, err
	}
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">` +
		`<d:prop><d:resourcetype/><d:displayname/><d:getetag/></d:prop></d:propfind>`
	res, err := c.doFollowRedirect(ctx, "PROPFIND", homeSet, body, "application/xml; charset=utf-8",
		map[string]string{"Depth": "1"})
	if err != nil {
		return nil, sanitizeCardDAVError(err)
	}
	if res.status < 200 || res.status >= 300 {
		return nil, fmt.Errorf("address book listing failed: %s", davStatusText(res.status))
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return nil, err
	}
	var out []addressBook
	for _, response := range multi.Responses {
		prop, ok := propstatOK(response.PropStats)
		if !ok {
			continue
		}
		if !strings.Contains(prop.ResourceTypeInner, "addressbook") {
			continue
		}
		name := strings.TrimSpace(prop.DisplayName)
		if name == "" {
			name = strings.Trim(strings.TrimSuffix(response.Href, "/"), "/")
			if idx := strings.LastIndex(name, "/"); idx >= 0 {
				name = name[idx+1:]
			}
		}
		out = append(out, addressBook{URL: c.resolve(response.Href), DisplayName: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no address books found")
	}
	return out, nil
}

func (c *carddavClient) findCurrentUserPrincipal(ctx context.Context) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	candidates := []string{c.base.String(), c.base.String() + "/.well-known/carddav"}
	var lastErr error
	for _, candidate := range candidates {
		res, err := c.doFollowRedirect(ctx, "PROPFIND", candidate, body, "application/xml; charset=utf-8",
			map[string]string{"Depth": "0"})
		if err != nil {
			lastErr = err
			continue
		}
		if res.status < 200 || res.status >= 300 {
			lastErr = fmt.Errorf("principal lookup failed: %s", davStatusText(res.status))
			continue
		}
		multi, err := parseMultistatus(res.body)
		if err != nil {
			lastErr = err
			continue
		}
		for _, response := range multi.Responses {
			if prop, ok := propstatOK(response.PropStats); ok && strings.TrimSpace(prop.Principal.Href) != "" {
				return c.resolve(prop.Principal.Href), nil
			}
		}
		lastErr = fmt.Errorf("server did not return a current-user-principal")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("CardDAV discovery failed")
	}
	return "", sanitizeCardDAVError(lastErr)
}

func (c *carddavClient) findAddressbookHomeSet(ctx context.Context, principalURL string) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">` +
		`<d:prop><card:addressbook-home-set/></d:prop></d:propfind>`
	res, err := c.doFollowRedirect(ctx, "PROPFIND", principalURL, body, "application/xml; charset=utf-8",
		map[string]string{"Depth": "0"})
	if err != nil {
		return "", sanitizeCardDAVError(err)
	}
	if res.status < 200 || res.status >= 300 {
		return "", fmt.Errorf("addressbook-home-set lookup failed: %s", davStatusText(res.status))
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return "", err
	}
	for _, response := range multi.Responses {
		if prop, ok := propstatOK(response.PropStats); ok && strings.TrimSpace(prop.AddressbookHomeSet.Href) != "" {
			return c.resolve(prop.AddressbookHomeSet.Href), nil
		}
	}
	return "", fmt.Errorf("server did not return an addressbook-home-set")
}

// syncChange is one vCard change reported by the server.
type syncChange struct {
	Href    string
	ETag    string
	VCard   []byte
	Deleted bool
}

// SyncAddressBook runs a sync-collection REPORT against one address book. When
// syncToken is empty the server returns every member; otherwise only changes
// since the token. Deleted members arrive as 404 responses.
func (c *carddavClient) SyncAddressBook(ctx context.Context, addressBookURL, syncToken string) ([]syncChange, string, error) {
	tokenXML := ""
	if strings.TrimSpace(syncToken) != "" {
		tokenXML = "<d:sync-token>" + xmlEscape(syncToken) + "</d:sync-token>"
	}
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:sync-collection xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">` +
		tokenXML +
		`<d:sync-level>1</d:sync-level>` +
		`<d:prop><d:getetag/><card:address-data/></d:prop>` +
		`</d:sync-collection>`
	res, err := c.do(ctx, "REPORT", addressBookURL, body, "application/xml; charset=utf-8",
		map[string]string{"Depth": "1"})
	if err != nil {
		return nil, "", sanitizeCardDAVError(err)
	}
	if res.status == http.StatusNotFound || res.status == http.StatusMethodNotAllowed ||
		res.status == http.StatusNotImplemented || res.status == http.StatusBadRequest {
		return nil, "", errSyncCollectionUnsupported
	}
	if res.status < 200 || res.status >= 300 {
		return nil, "", fmt.Errorf("sync-collection failed: %s", davStatusText(res.status))
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(multi.SyncToken) == "" {
		return nil, "", fmt.Errorf("server did not return a sync token")
	}
	var out []syncChange
	for _, response := range multi.Responses {
		href := c.resolve(response.Href)
		if strings.Contains(response.Status, " 404") {
			out = append(out, syncChange{Href: href, Deleted: true})
			continue
		}
		prop, ok := propstatOK(response.PropStats)
		if !ok || strings.TrimSpace(prop.AddressData) == "" {
			continue
		}
		out = append(out, syncChange{Href: href, ETag: strings.TrimSpace(prop.ETag), VCard: []byte(prop.AddressData)})
	}
	return out, strings.TrimSpace(multi.SyncToken), nil
}

// ListHrefs returns every member href of an address book for full-fetch fallback.
func (c *carddavClient) ListHrefs(ctx context.Context, addressBookURL string) ([]string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
	res, err := c.do(ctx, "PROPFIND", addressBookURL, body, "application/xml; charset=utf-8",
		map[string]string{"Depth": "1"})
	if err != nil {
		return nil, sanitizeCardDAVError(err)
	}
	if res.status < 200 || res.status >= 300 {
		return nil, fmt.Errorf("address book listing failed: %s", davStatusText(res.status))
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, response := range multi.Responses {
		href := c.resolve(response.Href)
		if strings.TrimRight(href, "/") == strings.TrimRight(addressBookURL, "/") {
			continue
		}
		out = append(out, href)
	}
	return out, nil
}

// MultigetVCards fetches vCard bodies for hrefs in batches.
func (c *carddavClient) MultigetVCards(ctx context.Context, addressBookURL string, hrefs []string) ([]syncChange, error) {
	var out []syncChange
	for start := 0; start < len(hrefs); start += 50 {
		end := start + 50
		if end > len(hrefs) {
			end = len(hrefs)
		}
		changes, err := c.multigetBatch(ctx, addressBookURL, hrefs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, changes...)
	}
	return out, nil
}

func (c *carddavClient) multigetBatch(ctx context.Context, addressBookURL string, hrefs []string) ([]syncChange, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<card:addressbook-multiget xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">`)
	b.WriteString(`<d:prop><d:getetag/><card:address-data/></d:prop>`)
	for _, href := range hrefs {
		b.WriteString("<d:href>")
		b.WriteString(xmlEscape(href))
		b.WriteString("</d:href>")
	}
	b.WriteString(`</card:addressbook-multiget>`)
	res, err := c.do(ctx, "REPORT", addressBookURL, b.String(), "application/xml; charset=utf-8",
		map[string]string{"Depth": "1"})
	if err != nil {
		return nil, sanitizeCardDAVError(err)
	}
	if res.status < 200 || res.status >= 300 {
		return nil, fmt.Errorf("addressbook-multiget failed: %s", davStatusText(res.status))
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return nil, err
	}
	var out []syncChange
	for _, response := range multi.Responses {
		href := c.resolve(response.Href)
		if strings.Contains(response.Status, " 404") {
			out = append(out, syncChange{Href: href, Deleted: true})
			continue
		}
		prop, ok := propstatOK(response.PropStats)
		if !ok || strings.TrimSpace(prop.AddressData) == "" {
			continue
		}
		out = append(out, syncChange{Href: href, ETag: strings.TrimSpace(prop.ETag), VCard: []byte(prop.AddressData)})
	}
	return out, nil
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func davStatusText(status int) string {
	if text := http.StatusText(status); text != "" {
		return fmt.Sprintf("HTTP %d %s", status, text)
	}
	return fmt.Sprintf("HTTP %d", status)
}

// sanitizeCardDAVError maps transport and auth failures to user-facing
// messages without leaking credentials or server internals.
func sanitizeCardDAVError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unauthorized") || strings.Contains(message, "401") ||
		strings.Contains(message, "forbidden") || strings.Contains(message, "403"):
		return fmt.Errorf("CardDAV authentication failed. Check the username and app password.")
	case strings.Contains(message, "certificate"), strings.Contains(message, "tls"):
		return fmt.Errorf("the CardDAV server's TLS connection could not be verified")
	case strings.Contains(message, "no such host"), strings.Contains(message, "connection refused"),
		strings.Contains(message, "no route to host"):
		return fmt.Errorf("the CardDAV server could not be reached")
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return fmt.Errorf("the CardDAV server timed out")
	default:
		return err
	}
}

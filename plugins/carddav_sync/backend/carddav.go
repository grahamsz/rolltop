// File overview: Minimal CardDAV client over raw net/http and encoding/xml.
// Discovery follows RFC 6764 (.well-known/carddav) to the principal's
// addressbook-home-set; incremental sync uses the sync-collection REPORT from
// RFC 6578 and falls back to a full multiget when the server does not support
// it. No third-party WebDAV dependency, to keep the plugin self-contained.

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const carddavRequestTimeout = 30 * time.Second

// errSyncCollectionUnsupported signals the caller to fall back to a full fetch.
var (
	errSyncCollectionUnsupported = errors.New("CardDAV sync-collection is not supported")
	errInvalidSyncToken          = errors.New("CardDAV sync token expired")
	errCardDAVAuth               = errors.New("CardDAV authentication failed. Check the username and app password.")
)

// isLoopbackHost reports whether a hostname is local-only, so local test
// servers can run over plain HTTP while remote servers require HTTPS.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("server URL is invalid")
	}
	if !isLoopbackHost(base.Hostname()) && !strings.EqualFold(base.Scheme, "https") {
		return nil, fmt.Errorf("CardDAV servers must use https, except on loopback hosts")
	}
	if strings.TrimSpace(creds.Username) == "" || creds.Password == "" {
		return nil, fmt.Errorf("username and password are required")
	}
	return &carddavClient{
		http:     &http.Client{Timeout: carddavRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
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
	ETag         string `xml:"getetag"`
	AddressData  string `xml:"address-data"`
	DisplayName  string `xml:"displayname"`
	ResourceType struct {
		AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
	} `xml:"resourcetype"`
	Principal struct {
		Href string `xml:"href"`
	} `xml:"current-user-principal"`
	AddressbookHomeSet struct {
		Href string `xml:"href"`
	} `xml:"addressbook-home-set"`
}

type davResult struct {
	url    string
	body   []byte
	header http.Header
	status int
}

func (c *carddavClient) do(ctx context.Context, method, rawURL, body, contentType string, headers map[string]string) (davResult, error) {
	if err := c.validateTarget(rawURL); err != nil {
		return davResult{}, sanitizeCardDAVError(err)
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return davResult{}, sanitizeCardDAVError(err)
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
		return davResult{}, sanitizeCardDAVError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return davResult{}, sanitizeCardDAVError(err)
	}
	if len(data) > 32<<20 {
		return davResult{}, errors.New("CardDAV response is too large")
	}
	return davResult{url: rawURL, body: data, header: resp.Header, status: resp.StatusCode}, nil
}

// doFollowRedirect replays a request across 301/302/307/308 redirects while
// preserving the DAV method and body, which net/http would otherwise downgrade
// to GET on some statuses.
func (c *carddavClient) doFollowRedirect(ctx context.Context, method, rawURL, body, contentType string, headers map[string]string) (davResult, error) {
	current := rawURL
	for redirects := 0; redirects < 5; redirects++ {
		res, err := c.do(ctx, method, current, body, contentType, headers)
		if err != nil {
			return davResult{}, sanitizeCardDAVError(err)
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
			return davResult{}, sanitizeCardDAVError(err)
		}
		ref, err := url.Parse(location)
		if err != nil {
			return davResult{}, sanitizeCardDAVError(err)
		}
		current = next.ResolveReference(ref).String()
	}
	return davResult{}, fmt.Errorf("too many redirects")
}

func carddavOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme + "://" + net.JoinHostPort(u.Hostname(), port))
}

func (c *carddavClient) validateTarget(raw string) error {
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Fragment != "" || carddavOrigin(target) != carddavOrigin(c.base) {
		return errors.New("CardDAV URLs and redirects must stay on the configured server origin")
	}
	return nil
}

func (c *carddavClient) resolveAt(baseURL, href string) (string, error) {
	if strings.TrimSpace(href) == "" {
		return "", errors.New("CardDAV response has no resource URL")
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", sanitizeCardDAVError(err)
	}
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", sanitizeCardDAVError(err)
	}
	target := base.ResolveReference(ref).String()
	if err := c.validateTarget(target); err != nil {
		return "", err
	}
	return target, nil
}

func responseStatus(value string) int {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(fields[1])
	return code
}

func davStatusError(operation string, status int) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return errCardDAVAuth
	}
	return fmt.Errorf("%s failed: %s", operation, davStatusText(status))
}

func parseMultistatus(data []byte) (*davMultistatus, error) {
	var out davMultistatus
	if err := xml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("could not parse DAV response: %w", err)
	}
	return &out, nil
}

func propstatOK(propstats []davPropstat) (davProp, bool) {
	var out davProp
	ok := false
	for _, ps := range propstats {
		if responseStatus(ps.Status) != http.StatusOK {
			continue
		}
		ok = true
		if ps.Prop.ETag != "" {
			out.ETag = ps.Prop.ETag
		}
		if ps.Prop.AddressData != "" {
			out.AddressData = ps.Prop.AddressData
		}
		if ps.Prop.DisplayName != "" {
			out.DisplayName = ps.Prop.DisplayName
		}
		if ps.Prop.ResourceType.AddressBook != nil {
			out.ResourceType = ps.Prop.ResourceType
		}
		if ps.Prop.Principal.Href != "" {
			out.Principal = ps.Prop.Principal
		}
		if ps.Prop.AddressbookHomeSet.Href != "" {
			out.AddressbookHomeSet = ps.Prop.AddressbookHomeSet
		}
	}
	return out, ok
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
		return nil, davStatusError("address book listing", res.status)
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
		if prop.ResourceType.AddressBook == nil {
			continue
		}
		name := strings.TrimSpace(prop.DisplayName)
		if name == "" {
			name = strings.Trim(strings.TrimSuffix(response.Href, "/"), "/")
			if idx := strings.LastIndex(name, "/"); idx >= 0 {
				name = name[idx+1:]
			}
		}
		target, err := c.resolveAt(res.url, response.Href)
		if err != nil {
			return nil, err
		}
		out = append(out, addressBook{URL: target, DisplayName: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no address books found")
	}
	return out, nil
}

func (c *carddavClient) findCurrentUserPrincipal(ctx context.Context) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	candidates := []string{c.base.String(), c.base.ResolveReference(&url.URL{Path: "/.well-known/carddav"}).String()}
	var lastErr error
	for _, candidate := range candidates {
		res, err := c.doFollowRedirect(ctx, "PROPFIND", candidate, body, "application/xml; charset=utf-8",
			map[string]string{"Depth": "0"})
		if err != nil {
			lastErr = err
			continue
		}
		if res.status < 200 || res.status >= 300 {
			lastErr = davStatusError("principal lookup", res.status)
			continue
		}
		multi, err := parseMultistatus(res.body)
		if err != nil {
			lastErr = err
			continue
		}
		for _, response := range multi.Responses {
			if prop, ok := propstatOK(response.PropStats); ok && strings.TrimSpace(prop.Principal.Href) != "" {
				return c.resolveAt(res.url, prop.Principal.Href)
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
		return "", davStatusError("addressbook-home-set lookup", res.status)
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return "", err
	}
	for _, response := range multi.Responses {
		if prop, ok := propstatOK(response.PropStats); ok && strings.TrimSpace(prop.AddressbookHomeSet.Href) != "" {
			return c.resolveAt(res.url, prop.AddressbookHomeSet.Href)
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
	// Servers may paginate a sync report with a 507 collection response. Do not
	// expose a new token until every page and changed card has been fetched.
	var out []syncChange
	token := syncToken
	seenTokens := map[string]bool{token: true}
	for page := 0; page < 100; page++ {
		body := `<?xml version="1.0" encoding="utf-8"?>` +
			`<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + xmlEscape(token) +
			`</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
		res, err := c.doFollowRedirect(ctx, "REPORT", addressBookURL, body, "application/xml; charset=utf-8", map[string]string{"Depth": "0"})
		if err != nil {
			return nil, "", err
		}
		if res.status == http.StatusForbidden {
			var failure struct {
				InvalidToken *struct{} `xml:"DAV: valid-sync-token"`
				Unsupported  *struct{} `xml:"DAV: supported-report"`
			}
			if xml.Unmarshal(res.body, &failure) == nil {
				if failure.InvalidToken != nil {
					return nil, "", errInvalidSyncToken
				}
				if failure.Unsupported != nil {
					return nil, "", errSyncCollectionUnsupported
				}
			}
		}
		switch res.status {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusBadRequest:
			return nil, "", errSyncCollectionUnsupported
		}
		if res.status != http.StatusMultiStatus {
			return nil, "", davStatusError("sync-collection", res.status)
		}
		multi, err := parseMultistatus(res.body)
		if err != nil {
			return nil, "", err
		}
		next := strings.TrimSpace(multi.SyncToken)
		if next == "" {
			return nil, "", errors.New("CardDAV response has no sync token")
		}
		more := false
		var fetch []string
		for _, response := range multi.Responses {
			href, err := c.resolveAt(res.url, response.Href)
			if err != nil {
				return nil, "", err
			}
			status := responseStatus(response.Status)
			if strings.TrimRight(href, "/") == strings.TrimRight(res.url, "/") && status == http.StatusInsufficientStorage {
				more = true
				continue
			}
			if status == http.StatusNotFound {
				out = append(out, syncChange{Href: href, Deleted: true})
				continue
			}
			if status != 0 && status != http.StatusOK {
				return nil, "", davStatusError("sync member", status)
			}
			prop, ok := propstatOK(response.PropStats)
			if !ok || strings.TrimSpace(prop.ETag) == "" {
				return nil, "", errors.New("CardDAV sync member is incomplete")
			}
			fetch = append(fetch, href)
		}
		cards, err := c.MultigetVCards(ctx, addressBookURL, fetch)
		if err != nil {
			return nil, "", err
		}
		out = append(out, cards...)
		if !more {
			return out, next, nil
		}
		if seenTokens[next] {
			return nil, "", errors.New("CardDAV pagination did not advance")
		}
		seenTokens[next] = true
		token = next
	}
	return nil, "", errors.New("CardDAV sync exceeded the page limit")
}

// ListHrefs returns an authoritative snapshot with ETags, so unchanged cards
// need no body download. Any failed member makes the snapshot incomplete.
func (c *carddavClient) ListHrefs(ctx context.Context, addressBookURL string) (map[string]string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
	res, err := c.doFollowRedirect(ctx, "PROPFIND", addressBookURL, body, "application/xml; charset=utf-8", map[string]string{"Depth": "1"})
	if err != nil {
		return nil, err
	}
	if res.status != http.StatusMultiStatus {
		return nil, davStatusError("address book listing", res.status)
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, response := range multi.Responses {
		href, err := c.resolveAt(res.url, response.Href)
		if err != nil {
			return nil, err
		}
		if strings.TrimRight(href, "/") == strings.TrimRight(res.url, "/") {
			continue
		}
		if status := responseStatus(response.Status); status != 0 && status != http.StatusOK {
			return nil, davStatusError("address book member", status)
		}
		prop, ok := propstatOK(response.PropStats)
		if !ok || strings.TrimSpace(prop.ETag) == "" {
			return nil, errors.New("CardDAV address book listing is incomplete")
		}
		out[href] = strings.TrimSpace(prop.ETag)
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
	res, err := c.doFollowRedirect(ctx, "REPORT", addressBookURL, b.String(), "application/xml; charset=utf-8",
		map[string]string{"Depth": "1"})
	if err != nil {
		return nil, sanitizeCardDAVError(err)
	}
	if res.status != http.StatusMultiStatus {
		return nil, davStatusError("addressbook-multiget", res.status)
	}
	multi, err := parseMultistatus(res.body)
	if err != nil {
		return nil, err
	}
	requested := map[string]bool{}
	for _, href := range hrefs {
		requested[href] = true
	}
	var out []syncChange
	for _, response := range multi.Responses {
		href, err := c.resolveAt(res.url, response.Href)
		if err != nil {
			return nil, err
		}
		if !requested[href] {
			return nil, errors.New("CardDAV multiget returned an unexpected or duplicate resource")
		}
		delete(requested, href)
		status := responseStatus(response.Status)
		if status == http.StatusNotFound {
			// It vanished after listing. Retry the snapshot instead of assuming the
			// partially fetched address book is authoritative for local removals.
			return nil, errors.New("CardDAV address book changed during fetch; retrying")
		}
		if status != 0 && status != http.StatusOK {
			return nil, davStatusError("CardDAV multiget member", status)
		}
		prop, ok := propstatOK(response.PropStats)
		if !ok || strings.TrimSpace(prop.AddressData) == "" || strings.TrimSpace(prop.ETag) == "" {
			return nil, errors.New("CardDAV multiget member is incomplete")
		}
		out = append(out, syncChange{Href: href, ETag: strings.TrimSpace(prop.ETag), VCard: []byte(prop.AddressData)})
	}
	if len(requested) != 0 {
		return nil, errors.New("CardDAV multiget omitted requested resources")
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
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var transport *url.Error
	if errors.As(err, &transport) {
		// Never surface the URL: providers can use tokens in paths or queries.
		return errors.New("the CardDAV connection failed; check the server URL and TLS settings")
	}
	return err
}

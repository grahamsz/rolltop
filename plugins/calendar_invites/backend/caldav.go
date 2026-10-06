// File overview: Minimal CalDAV client over raw net/http and encoding/xml.
// Discovery follows RFC 6764 (.well-known/caldav) to the principal's
// calendar-home-set, then lists calendars. Event upload is a plain HTTP PUT
// of the iCalendar object to {calendar-url}/{sha256(UID)}.ics, which is idempotent:
// re-pushing the same UID updates the event instead of duplicating it. No
// third-party WebDAV dependency, to keep the plugin self-contained. The
// request plumbing mirrors the carddav_sync plugin's CardDAV client.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const caldavRequestTimeout = 30 * time.Second

// caldavCredentials carries the CalDAV login. The password must never be
// logged; only the username appears in errors.
type caldavCredentials struct {
	Username string
	Password string
}

type caldavClient struct {
	http     *http.Client
	base     *url.URL
	username string
	password string
}

func newCalDAVClient(serverURL string, creds caldavCredentials) (*caldavClient, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("server URL is required")
	}
	base, err := url.Parse(trimmed)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("server URL is invalid")
	}
	if !isLoopbackHost(base.Hostname()) && !strings.EqualFold(base.Scheme, "https") {
		return nil, fmt.Errorf("CalDAV servers must use https, except on loopback hosts")
	}
	if strings.TrimSpace(creds.Username) == "" || creds.Password == "" {
		return nil, fmt.Errorf("username and password are required")
	}
	return &caldavClient{
		http:     &http.Client{Timeout: caldavRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base:     base,
		username: strings.TrimSpace(creds.Username),
		password: creds.Password,
	}, nil
}

// isLoopbackHost reports whether a hostname is local-only, so local test
// servers can run over plain HTTP while remote servers require HTTPS.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// calendar is one discovered CalDAV calendar.
type calendar struct {
	URL         string
	DisplayName string
}

// caldavMultistatus decodes a DAV multistatus body. Struct tags match local
// element names, so namespace prefixes are ignored by encoding/xml.
type caldavMultistatus struct {
	XMLName   xml.Name         `xml:"multistatus"`
	Responses []caldavResponse `xml:"response"`
}

type caldavResponse struct {
	Href      string           `xml:"href"`
	Status    string           `xml:"status"`
	PropStats []caldavPropstat `xml:"propstat"`
}

type caldavPropstat struct {
	Prop   caldavProp `xml:"prop"`
	Status string     `xml:"status"`
}

type caldavProp struct {
	DisplayName string `xml:"displayname"`
	// Match the CalDAV namespace URI independently of the server's XML prefix.
	ResourceType struct {
		Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	} `xml:"resourcetype"`
	Principal struct {
		Href string `xml:"href"`
	} `xml:"current-user-principal"`
	CalendarHomeSet struct {
		Href string `xml:"href"`
	} `xml:"calendar-home-set"`
}

type caldavResult struct {
	body   []byte
	header http.Header
	status int
}

func (c *caldavClient) do(ctx context.Context, method, rawURL, body, contentType string, headers map[string]string) (caldavResult, error) {
	if err := c.validateTarget(rawURL); err != nil {
		return caldavResult{}, err
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return caldavResult{}, sanitizeCalDAVError(err)
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("User-Agent", "Rolltop-Calendar-Invites/1.0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return caldavResult{}, sanitizeCalDAVError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return caldavResult{}, sanitizeCalDAVError(err)
	}
	return caldavResult{body: data, header: resp.Header, status: resp.StatusCode}, nil
}

// doFollowRedirect replays a request across 301/302/307/308 redirects while
// preserving the DAV method and body, which net/http would otherwise downgrade
// to GET on some statuses.
func (c *caldavClient) doFollowRedirect(ctx context.Context, method, rawURL, body, contentType string, headers map[string]string) (caldavResult, error) {
	current := rawURL
	for redirects := 0; redirects < 5; redirects++ {
		res, err := c.do(ctx, method, current, body, contentType, headers)
		if err != nil {
			return caldavResult{}, sanitizeCalDAVError(err)
		}
		if res.status != http.StatusMovedPermanently && res.status != http.StatusFound &&
			res.status != http.StatusTemporaryRedirect && res.status != http.StatusPermanentRedirect {
			return res, nil
		}
		location := strings.TrimSpace(res.header.Get("Location"))
		if location == "" {
			return caldavResult{}, fmt.Errorf("redirect without Location header")
		}
		next, err := url.Parse(current)
		if err != nil {
			return caldavResult{}, sanitizeCalDAVError(err)
		}
		ref, err := url.Parse(location)
		if err != nil {
			return caldavResult{}, sanitizeCalDAVError(err)
		}
		current = next.ResolveReference(ref).String()
	}
	return caldavResult{}, fmt.Errorf("too many redirects")
}

func (c *caldavClient) resolve(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return c.base.ResolveReference(ref).String()
}

func parseCalDAVMultistatus(data []byte) (*caldavMultistatus, error) {
	var out caldavMultistatus
	if err := xml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("could not parse DAV response: %w", err)
	}
	return &out, nil
}

func caldavPropstatOK(propstats []caldavPropstat) (caldavProp, bool) {
	for _, ps := range propstats {
		if strings.Contains(ps.Status, " 200") || strings.TrimSpace(ps.Status) == "" {
			return ps.Prop, true
		}
	}
	return caldavProp{}, false
}

// sanitizeCalDAVError is the choke point for CalDAV errors reaching the API.
// Transport errors can include URLs with private calendar paths or tokens.
func sanitizeCalDAVError(err error) error {
	// net/http errors include request URLs, which may contain capability tokens.
	if err == nil {
		return nil
	}
	return fmt.Errorf("CalDAV request failed (%T)", err)
}

func caldavOrigin(u *url.URL) string {
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

func (c *caldavClient) validateTarget(raw string) error {
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Fragment != "" || caldavOrigin(target) != caldavOrigin(c.base) {
		return fmt.Errorf("CalDAV URLs and redirects must stay on the configured server origin")
	}
	return nil
}

// davStatusText renders an HTTP status for API errors.
func davStatusText(status int) string {
	if text := http.StatusText(status); text != "" {
		return fmt.Sprintf("%d %s", status, text)
	}
	return fmt.Sprintf("%d", status)
}

// DiscoverCalendars walks well-known → current-user-principal →
// calendar-home-set → calendars (RFC 6764, RFC 4791 section 6).
func (c *caldavClient) DiscoverCalendars(ctx context.Context) ([]calendar, error) {
	principal, err := c.findCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	homeSet, err := c.findCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, err
	}
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">` +
		`<d:prop><d:resourcetype/><d:displayname/><d:getetag/></d:prop></d:propfind>`
	res, err := c.doFollowRedirect(ctx, "PROPFIND", homeSet, body, "application/xml; charset=utf-8",
		map[string]string{"Depth": "1"})
	if err != nil {
		return nil, sanitizeCalDAVError(err)
	}
	if res.status < 200 || res.status >= 300 {
		return nil, fmt.Errorf("calendar listing failed: %s", davStatusText(res.status))
	}
	multi, err := parseCalDAVMultistatus(res.body)
	if err != nil {
		return nil, err
	}
	var out []calendar
	for _, response := range multi.Responses {
		prop, ok := caldavPropstatOK(response.PropStats)
		if !ok {
			continue
		}
		if prop.ResourceType.Calendar == nil {
			continue
		}
		name := strings.TrimSpace(prop.DisplayName)
		if name == "" {
			name = strings.Trim(strings.TrimSuffix(response.Href, "/"), "/")
			if idx := strings.LastIndex(name, "/"); idx >= 0 {
				name = name[idx+1:]
			}
		}
		out = append(out, calendar{URL: c.resolve(response.Href), DisplayName: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no calendars found")
	}
	return out, nil
}

func (c *caldavClient) findCurrentUserPrincipal(ctx context.Context) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	candidates := []string{c.base.String(), c.base.ResolveReference(&url.URL{Path: "/.well-known/caldav"}).String()}
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
		multi, err := parseCalDAVMultistatus(res.body)
		if err != nil {
			lastErr = err
			continue
		}
		for _, response := range multi.Responses {
			if prop, ok := caldavPropstatOK(response.PropStats); ok && strings.TrimSpace(prop.Principal.Href) != "" {
				return c.resolve(prop.Principal.Href), nil
			}
		}
		lastErr = fmt.Errorf("server did not return a current-user-principal")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("CalDAV discovery failed")
	}
	return "", sanitizeCalDAVError(lastErr)
}

func (c *caldavClient) findCalendarHomeSet(ctx context.Context, principalURL string) (string, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">` +
		`<d:prop><cal:calendar-home-set/></d:prop></d:propfind>`
	res, err := c.doFollowRedirect(ctx, "PROPFIND", principalURL, body, "application/xml; charset=utf-8",
		map[string]string{"Depth": "0"})
	if err != nil {
		return "", sanitizeCalDAVError(err)
	}
	if res.status < 200 || res.status >= 300 {
		return "", fmt.Errorf("calendar-home-set lookup failed: %s", davStatusText(res.status))
	}
	multi, err := parseCalDAVMultistatus(res.body)
	if err != nil {
		return "", err
	}
	for _, response := range multi.Responses {
		if prop, ok := caldavPropstatOK(response.PropStats); ok && strings.TrimSpace(prop.CalendarHomeSet.Href) != "" {
			return c.resolve(prop.CalendarHomeSet.Href), nil
		}
	}
	return "", fmt.Errorf("server did not return a calendar-home-set")
}

// PutEvent uploads one iCalendar object to the calendar. The object URL is
// derived from the event UID, so pushing twice updates rather than duplicates.
func (c *caldavClient) PutEvent(ctx context.Context, calendarURL, uid string, ics []byte) error {
	calendars := parseICSCalendars(ics)
	if len(calendars) != 1 {
		return fmt.Errorf("invalid calendar payload")
	}
	calendar := calendars[0]
	properties := calendar.props[:0]
	for _, prop := range calendar.props {
		if prop.Name != "METHOD" {
			properties = append(properties, prop)
		}
	}
	calendar.props = properties
	// CalDAV calendar objects cannot carry scheduling METHOD (RFC 4791 4.1).
	ics = []byte(calendar.render())
	target := strings.TrimRight(calendarURL, "/") + "/" + fmt.Sprintf("%x.ics", sha256.Sum256([]byte(uid)))
	res, err := c.doFollowRedirect(ctx, "PUT", target, string(ics), "text/calendar; charset=utf-8", nil)
	if err != nil {
		return sanitizeCalDAVError(err)
	}
	if res.status == http.StatusOK || res.status == http.StatusCreated || res.status == http.StatusNoContent {
		return nil
	}
	return fmt.Errorf("calendar upload failed: %s", davStatusText(res.status))
}

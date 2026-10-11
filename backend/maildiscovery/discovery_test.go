package maildiscovery

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

type fakeResolver struct {
	srv map[string][]*net.SRV
	ips []netip.Addr
}

func (r fakeResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return "", r.srv[service+"."+name], ctx.Err()
}
func (r fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return r.ips, ctx.Err()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testDiscoverer(fn roundTripFunc) *Discoverer {
	return &Discoverer{client: &http.Client{Transport: fn, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, resolver: fakeResolver{}}
}

func response(r *http.Request, code int, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func thunderbird(imapHost, smtpHost string) string {
	return fmt.Sprintf(`<clientConfig version="1.1"><emailProvider id="example.com">
<incomingServer type="imap"><hostname>%s</hostname><port>993</port><socketType>SSL</socketType><authentication>password-cleartext</authentication><username>%%EMAILLOCALPART%%</username></incomingServer>
<outgoingServer type="smtp"><hostname>%s</hostname><port>587</port><socketType>STARTTLS</socketType><authentication>password-cleartext</authentication><username>%%EMAILADDRESS%%</username></outgoingServer>
</emailProvider></clientConfig>`, imapHost, smtpHost)
}

func microsoftXML(account string) string {
	return `<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006"><Response xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a"><Account>` + account + `</Account></Response></Autodiscover>`
}

const microsoftProtocols = `<Action>settings</Action><Protocol><Type>IMAP</Type><Server>imap.microsoft.example</Server><Port>993</Port><SSL>on</SSL><LoginName>mail-user</LoginName></Protocol><Protocol><Type>SMTP</Type><Server>smtp.microsoft.example</Server><Port>587</Port><SSL>on</SSL></Protocol>`

func TestDiscoverPrefersProviderAndNeverSendsCredentials(t *testing.T) {
	var mu sync.Mutex
	var requests int
	d := testDiscoverer(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		requests++
		mu.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.RawQuery != "" {
			t.Errorf("discovery sent credentials or query: %s", r.URL)
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "alice@example.com") || strings.Contains(string(body), "Password") {
				t.Errorf("unexpected Autodiscover request: %s", body)
			}
			return response(r, 200, microsoftXML(microsoftProtocols)), nil
		}
		if r.URL.Host == "autoconfig.example.com" {
			return response(r, 200, thunderbird("imap.%EMAILDOMAIN%", "smtp.example.com")), nil
		}
		return response(r, 200, thunderbird("imap.fallback.example", "smtp.fallback.example")), nil
	})
	got, err := d.Discover(context.Background(), "alice@example.com")
	if err != nil || got.IMAP == nil || got.SMTP == nil {
		t.Fatalf("result = %+v, %v", got, err)
	}
	if got.IMAP.Host != "imap.example.com" || got.IMAP.Username != "alice" || got.SMTP.Host != "smtp.example.com" || got.SMTP.Username != "alice@example.com" {
		t.Fatalf("wrong discovered settings: %+v / %+v", got.IMAP, got.SMTP)
	}
	if requests != 4 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestAutoconfigFallbacksAndPartialResults(t *testing.T) {
	for _, target := range []string{"example.com", "autoconfig.thunderbird.net"} {
		t.Run(target, func(t *testing.T) {
			d := testDiscoverer(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet && r.URL.Host == target {
					return response(r, 200, thunderbird("imap.example.com", "invalid")), nil
				}
				return response(r, 404, "missing"), nil
			})
			d.resolver = fakeResolver{srv: map[string][]*net.SRV{"submission.example.com": {{Target: "submission.example.com.", Port: 587}}}}
			got, err := d.Discover(context.Background(), "alice@example.com")
			if err != nil || got.IMAP == nil || got.SMTP == nil || got.SMTP.Source != "DNS SRV" || got.SMTP.Host != "submission.example.com" {
				t.Fatalf("fallback result = %+v, %v", got, err)
			}
		})
	}
}

func TestAutoconfigRejectsUnsupportedSettingsAndMalformedXML(t *testing.T) {
	base := thunderbird("imap.example.com", "smtp.example.com")
	for name, doc := range map[string]string{
		"OAuth only":      strings.ReplaceAll(base, "password-cleartext", "OAuth2"),
		"plaintext":       strings.NewReplacer("<socketType>SSL", "<socketType>plain", "<socketType>STARTTLS", "<socketType>plain").Replace(base),
		"invalid ports":   strings.NewReplacer("<port>993", "<port>0", "<port>587", "<port>65536").Replace(base),
		"invalid XML":     base + "<broken>",
		"wrong root":      strings.ReplaceAll(base, "clientConfig", "untrusted"),
		"unknown version": strings.Replace(base, `version="1.1"`, `version="99"`, 1),
		"entity":          `<!DOCTYPE clientConfig [<!ENTITY secret SYSTEM "file:///etc/passwd">]>` + strings.ReplaceAll(base, "%EMAILADDRESS%", "&secret;"),
	} {
		t.Run(name, func(t *testing.T) {
			got := parseAutoconfig([]byte(doc), "alice@example.com", "test")
			if got.IMAP != nil || got.SMTP != nil {
				t.Fatalf("unsupported settings accepted: %+v", got)
			}
		})
	}
	got := parseAutoconfig([]byte(strings.Replace(base, "<socketType>SSL", "<socketType>STARTTLS", 1)), "alice@example.com", "test")
	if got.IMAP != nil || got.SMTP == nil {
		t.Fatal("IMAP STARTTLS must not be treated as implicit TLS")
	}
	got = parseAutoconfig([]byte(strings.ReplaceAll(base, "<authentication>password-cleartext", "<authentication>OAuth2</authentication><authentication>password-cleartext")), "alice@example.com", "test")
	if got.IMAP == nil || got.SMTP == nil {
		t.Fatal("password alternative was not selected")
	}
}

func TestMicrosoftDiscoveryEndpointsAndRedirects(t *testing.T) {
	for _, mode := range []string{"domain", "subdomain", "srv", "http redirect", "xml redirect", "address redirect"} {
		t.Run(mode, func(t *testing.T) {
			d := testDiscoverer(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodPost {
					return response(r, 404, ""), nil
				}
				body, _ := io.ReadAll(r.Body)
				var request struct {
					Email string `xml:"Request>EMailAddress"`
				}
				if xml.Unmarshal(body, &request) != nil {
					t.Errorf("invalid request: %s", body)
					return nil, errors.New("invalid request")
				}
				if r.URL.Host == "example.com" {
					switch mode {
					case "domain":
						return response(r, 200, microsoftXML(microsoftProtocols)), nil
					case "http redirect":
						resp := response(r, 302, "")
						resp.Header.Set("Location", "https://redirect.example/autodiscover.xml")
						return resp, nil
					case "xml redirect":
						return response(r, 200, microsoftXML("<Action>redirectUrl</Action><RedirectUrl>https://redirect.example/autodiscover.xml</RedirectUrl>")), nil
					case "address redirect":
						return response(r, 200, microsoftXML("<Action>redirectAddr</Action><RedirectAddr>alias@redirect.example</RedirectAddr>")), nil
					}
				}
				if mode == "subdomain" && r.URL.Host == "autodiscover.example.com" || mode == "srv" && r.URL.Host == "srv.example.com" || r.URL.Host == "redirect.example" {
					if mode == "address redirect" && request.Email != "alias@redirect.example" {
						t.Errorf("redirect email = %q", request.Email)
						return nil, errors.New("wrong redirected email")
					}
					return response(r, 200, microsoftXML(microsoftProtocols)), nil
				}
				return response(r, 404, ""), nil
			})
			d.resolver = fakeResolver{srv: map[string][]*net.SRV{"autodiscover.example.com": {{Target: "srv.example.com.", Port: 443}}}}
			got, err := d.Discover(context.Background(), "alice@example.com")
			if err != nil || got.IMAP == nil || got.SMTP == nil {
				t.Fatalf("result = %+v, %v", got, err)
			}
			if got.IMAP.Host != "imap.microsoft.example" || got.IMAP.Username != "mail-user" || got.SMTP.Security != "starttls" {
				t.Fatalf("bad Microsoft result: %+v / %+v", got.IMAP, got.SMTP)
			}
		})
	}
}

func TestMicrosoftRefusesUnsupportedAndUnsafeResponses(t *testing.T) {
	for name, body := range map[string]string{
		"Exchange only":   microsoftXML(`<Action>settings</Action><Protocol><Type>EXCH</Type><Server>exchange.example.com</Server></Protocol>`),
		"plaintext":       microsoftXML(strings.ReplaceAll(microsoftProtocols, "<SSL>on", "<SSL>off")),
		"SPA":             microsoftXML(strings.ReplaceAll(microsoftProtocols, "<SSL>", "<SPA>on</SPA><SSL>")),
		"HTTP redirect":   microsoftXML(`<Action>redirectUrl</Action><RedirectUrl>http://unsafe.example/config.xml</RedirectUrl>`),
		"URL credentials": microsoftXML(`<Action>redirectUrl</Action><RedirectUrl>https://user:password@unsafe.example/config.xml</RedirectUrl>`),
		"IP redirect":     microsoftXML(`<Action>redirectUrl</Action><RedirectUrl>https://127.0.0.1/config.xml</RedirectUrl>`),
		"loop":            microsoftXML(`<Action>redirectAddr</Action><RedirectAddr>alice@example.com</RedirectAddr>`),
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			d := testDiscoverer(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Host == "unsafe.example" {
					t.Fatal("followed unsafe redirect")
				}
				return response(r, 200, body), nil
			})
			got := d.microsoft(context.Background(), "alice@example.com", "example.com")
			if got.IMAP != nil || got.SMTP != nil || requests > 12 {
				t.Fatalf("unsafe result = %+v, requests = %d", got, requests)
			}
		})
	}
}

func TestRequestEscapesEmail(t *testing.T) {
	body := microsoftRequest("a&b@example.com")
	var request struct {
		Email string `xml:"Request>EMailAddress"`
	}
	if err := xml.Unmarshal([]byte(body), &request); err != nil || request.Email != "a&b@example.com" {
		t.Fatalf("request = %s, %v", body, err)
	}
}

func TestDiscoveryValidatesBeforeNetworkAndSupportsCancellation(t *testing.T) {
	d := testDiscoverer(func(r *http.Request) (*http.Response, error) {
		t.Fatal("network called for invalid email")
		return nil, errors.New("unexpected")
	})
	for _, email := range []string{"", "alice", "Alice <alice@example.com>", "alice@127.0.0.1", "alice@localhost", "alice@host/path.example", "alice@foo..example", "alice@example.com\nforged"} {
		if _, err := d.Discover(context.Background(), email); !errors.Is(err, ErrInvalidEmail) {
			t.Fatalf("email %q: %v", email, err)
		}
	}
	d = testDiscoverer(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := d.Discover(ctx, "alice@example.com")
	if err != nil || got.IMAP != nil || got.SMTP != nil {
		t.Fatalf("canceled result = %+v, %v", got, err)
	}
}

func TestDiscoveryResponseAndRedirectLimits(t *testing.T) {
	for _, mode := range []string{"large", "redirect loop", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			d := testDiscoverer(func(r *http.Request) (*http.Response, error) {
				requests++
				if mode == "large" {
					return response(r, 200, strings.Repeat("x", maxResponseBytes+1)), nil
				}
				if mode == "unauthorized" {
					return response(r, 401, ""), nil
				}
				resp := response(r, 307, "")
				resp.Header.Set("Location", r.URL.String())
				return resp, nil
			})
			got := d.autoconfig(context.Background(), "https://autoconfig.example.com/config.xml", "alice@example.com", "test")
			if got.IMAP != nil || got.SMTP != nil || requests > maxRedirects+1 {
				t.Fatalf("result = %+v, requests = %d", got, requests)
			}
		})
	}
}

func TestSafeDialBlocksPrivateNetworksAndPinsDNS(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "192.0.2.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::7f00:1"} {
		r := fakeResolver{ips: []netip.Addr{netip.MustParseAddr(ip)}}
		_, err := safeDial(context.Background(), r, func(context.Context, string, string) (net.Conn, error) {
			t.Fatalf("dialed blocked IP %s", ip)
			return nil, nil
		}, "tcp", "autoconfig.example.com:443")
		if err == nil {
			t.Fatalf("accepted blocked IP %s", ip)
		}
	}
	r := fakeResolver{ips: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("8.8.8.8")}}
	_, err := safeDial(context.Background(), r, func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			t.Fatalf("dialed unvalidated target %q", address)
		}
		return nil, nil
	}, "tcp", "autoconfig.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryURLRestrictions(t *testing.T) {
	for _, raw := range []string{"http://example.com/config", "https://example.com:8443/config", "https://user:pass@example.com/config", "https://127.0.0.1/config", "https://[::1]/config", "https://localhost/config", "https://example.com/config#fragment", "file:///etc/passwd"} {
		if _, err := discoveryURL(raw); err == nil {
			t.Fatalf("accepted unsafe URL %q", raw)
		}
	}
}

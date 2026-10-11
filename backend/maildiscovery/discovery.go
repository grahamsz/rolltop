// Package maildiscovery discovers public IMAP/SMTP settings without credentials.
package maildiscovery

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"
)

const Timeout = 12 * time.Second

var ErrInvalidEmail = errors.New("enter a valid email address with a public DNS domain")

type Server struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Security string `json:"security"`
	Source   string `json:"source"`
}

type Result struct {
	IMAP *Server `json:"imap,omitempty"`
	SMTP *Server `json:"smtp,omitempty"`
}

type resolver interface {
	LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error)
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Discoverer struct {
	client   *http.Client
	resolver resolver
}

func New() *Discoverer {
	r := net.DefaultResolver
	return &Discoverer{client: newHTTPClient(r), resolver: r}
}

func emailParts(value string) (email, local, domain string, err error) {
	email = strings.TrimSpace(value)
	parsed, parseErr := mail.ParseAddress(email)
	at := strings.LastIndexByte(email, '@')
	if parseErr != nil || parsed.Address != email || at <= 0 || len(email) > 254 {
		return "", "", "", ErrInvalidEmail
	}
	local, domain = email[:at], strings.ToLower(email[at+1:])
	if !validHost(domain) || !strings.Contains(domain, ".") {
		return "", "", "", ErrInvalidEmail
	}
	return email, local, domain, nil
}

func validHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// Discover has a total deadline and a fixed set of public discovery sources.
// Results are suggestions only: this never connects to mail servers or saves data.
func (d *Discoverer) Discover(ctx context.Context, value string) (Result, error) {
	email, _, domain, err := emailParts(value)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// Run independent discovery mechanisms together so a slow provider cannot
	// consume the entire deadline before the other formats have a chance.
	type outcome struct {
		priority int
		result   Result
	}
	jobs := []func(context.Context) Result{
		func(ctx context.Context) Result {
			return d.autoconfig(ctx, "https://autoconfig."+domain+"/mail/config-v1.1.xml", email, "Provider autoconfig")
		},
		func(ctx context.Context) Result {
			return d.autoconfig(ctx, "https://"+domain+"/.well-known/autoconfig/mail/config-v1.1.xml", email, "Provider autoconfig")
		},
		func(ctx context.Context) Result { return d.microsoft(ctx, email, domain) },
		func(ctx context.Context) Result {
			return d.autoconfig(ctx, "https://autoconfig.thunderbird.net/v1.1/"+domain, email, "Thunderbird ISPDB")
		},
		func(ctx context.Context) Result { return d.mailSRV(ctx, email, domain) },
	}
	ch := make(chan outcome, len(jobs))
	for i, job := range jobs {
		go func() { ch <- outcome{i, job(ctx)} }()
	}
	results := make([]Result, len(jobs))
	for range jobs {
		got := <-ch
		results[got.priority] = got.result
	}
	var out Result
	for _, result := range results {
		if out.IMAP == nil {
			out.IMAP = result.IMAP
		}
		if out.SMTP == nil {
			out.SMTP = result.SMTP
		}
	}
	return out, nil
}

func (d *Discoverer) mailSRV(ctx context.Context, email, domain string) Result {
	var out Result
	for _, spec := range []struct{ service, kind, security string }{
		{"imaps", "imap", "tls"}, {"submissions", "smtp", "tls"}, {"submission", "smtp", "starttls"},
	} {
		if spec.kind == "smtp" && out.SMTP != nil {
			continue
		}
		_, records, err := d.lookupSRV(ctx, spec.service, domain)
		if err != nil {
			continue
		}
		// net.Resolver orders records by priority and randomizes by weight.
		for _, record := range records {
			server := supportedServer(spec.kind, strings.TrimSuffix(record.Target, "."), int(record.Port), email, spec.security, "DNS SRV")
			if server == nil {
				continue
			}
			if spec.kind == "imap" {
				out.IMAP = server
			} else {
				out.SMTP = server
			}
			break
		}
	}
	return out
}

func (d *Discoverer) lookupSRV(ctx context.Context, service, domain string) (string, []*net.SRV, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return d.resolver.LookupSRV(ctx, service, "tcp", domain)
}

func supportedServer(kind, host string, port int, username, security, source string) *Server {
	host = strings.ToLower(strings.TrimSpace(host))
	username = strings.TrimSpace(username)
	if !validHost(host) || !strings.Contains(host, ".") || port < 1 || port > 65535 || username == "" || strings.ContainsAny(username, "\r\n\x00") {
		return nil
	}
	// Match the transports supported by the existing clients. IMAP currently
	// supports implicit TLS only; SMTP uses implicit TLS on 465 and STARTTLS
	// elsewhere. Never silently downgrade unsupported settings to cleartext.
	if kind == "imap" && security != "tls" {
		return nil
	}
	if kind == "smtp" && !((security == "tls" && port == 465) || (security == "starttls" && port != 465)) {
		return nil
	}
	return &Server{Host: host, Port: port, Username: username, Security: security, Source: source}
}

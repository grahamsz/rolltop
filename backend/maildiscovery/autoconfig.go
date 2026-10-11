package maildiscovery

import (
	"context"
	"encoding/xml"
	"regexp"
	"strings"
)

var unresolvedPlaceholder = regexp.MustCompile(`%[A-Z][A-Z0-9_]*%`)

type autoconfigDocument struct {
	XMLName  xml.Name `xml:"clientConfig"`
	Version  string   `xml:"version,attr"`
	Provider struct {
		Incoming []autoconfigServer `xml:"incomingServer"`
		Outgoing []autoconfigServer `xml:"outgoingServer"`
	} `xml:"emailProvider"`
}

type autoconfigServer struct {
	Type           string   `xml:"type,attr"`
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	SocketType     string   `xml:"socketType"`
	Username       string   `xml:"username"`
	Authentication []string `xml:"authentication"`
}

func (d *Discoverer) autoconfig(ctx context.Context, target, email, source string) Result {
	for redirects := 0; redirects <= maxRedirects; redirects++ {
		data, redirect, err := d.fetch(ctx, target, "")
		if err != nil {
			return Result{}
		}
		if redirect != "" {
			target = redirect
			continue
		}
		return parseAutoconfig(data, email, source)
	}
	return Result{}
}

func parseAutoconfig(data []byte, email, source string) Result {
	var doc autoconfigDocument
	if !decodeDocument(data, &doc) || doc.Version != "1.1" {
		return Result{}
	}
	_, local, domain, err := emailParts(email)
	if err != nil {
		return Result{}
	}
	expand := func(value string) string {
		return strings.NewReplacer("%EMAILADDRESS%", email, "%EMAILLOCALPART%", local, "%EMAILDOMAIN%", domain).Replace(strings.TrimSpace(value))
	}
	choose := func(servers []autoconfigServer, kind string) *Server {
		for _, server := range servers {
			if server.Type != kind {
				continue
			}
			passwordAuth := false
			for _, method := range server.Authentication {
				if strings.EqualFold(strings.TrimSpace(method), "password-cleartext") {
					passwordAuth = true
				}
			}
			if !passwordAuth {
				continue
			}
			security := ""
			switch strings.ToUpper(strings.TrimSpace(server.SocketType)) {
			case "SSL":
				security = "tls"
			case "STARTTLS":
				security = "starttls"
			}
			host, username := expand(server.Hostname), expand(server.Username)
			if unresolvedPlaceholder.MatchString(username) {
				continue
			}
			if found := supportedServer(kind, host, server.Port, username, security, source); found != nil {
				return found
			}
		}
		return nil
	}
	return Result{IMAP: choose(doc.Provider.Incoming, "imap"), SMTP: choose(doc.Provider.Outgoing, "smtp")}
}

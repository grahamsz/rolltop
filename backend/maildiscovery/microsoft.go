package maildiscovery

import (
	"bytes"
	"context"
	"encoding/xml"
	"strings"
)

const autodiscoverPath = "/autodiscover/autodiscover.xml"

type microsoftDocument struct {
	XMLName xml.Name `xml:"Autodiscover"`
	Account struct {
		Action          string `xml:"Action"`
		RedirectURL     string `xml:"RedirectUrl"`
		RedirectAddress string `xml:"RedirectAddr"`
		Protocols       []struct {
			Type        string `xml:"Type"`
			Server      string `xml:"Server"`
			Port        int    `xml:"Port"`
			LoginName   string `xml:"LoginName"`
			SSL         string `xml:"SSL"`
			Encryption  string `xml:"Encryption"`
			SPA         string `xml:"SPA"`
			AuthPackage string `xml:"AuthPackage"`
		} `xml:"Protocol"`
	} `xml:"Response>Account"`
}

func microsoftRequest(email string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(email))
	return `<?xml version="1.0" encoding="utf-8"?><Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006"><Request><EMailAddress>` + escaped.String() + `</EMailAddress><AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a</AcceptableResponseSchema></Request></Autodiscover>`
}

func (d *Discoverer) microsoft(ctx context.Context, email, domain string) Result {
	budget := 12
	return d.microsoftDomain(ctx, email, domain, maxRedirects, &budget)
}

func (d *Discoverer) microsoftDomain(ctx context.Context, email, domain string, redirects int, budget *int) Result {
	targets := []string{"https://" + domain + autodiscoverPath, "https://autodiscover." + domain + autodiscoverPath}
	_, records, _ := d.lookupSRV(ctx, "autodiscover", domain)
	for _, record := range records {
		host := strings.TrimSuffix(record.Target, ".")
		if record.Port == 443 && validHost(host) {
			targets = append(targets, "https://"+host+autodiscoverPath)
			if len(targets) >= 4 {
				break
			}
		}
	}
	var out Result
	for _, target := range targets {
		result := d.microsoftAt(ctx, target, email, redirects, budget)
		if out.IMAP == nil {
			out.IMAP = result.IMAP
		}
		if out.SMTP == nil {
			out.SMTP = result.SMTP
		}
		if out.IMAP != nil && out.SMTP != nil {
			break
		}
	}
	return out
}

func (d *Discoverer) microsoftAt(ctx context.Context, target, email string, redirects int, budget *int) Result {
	if redirects < 0 || *budget <= 0 || ctx.Err() != nil {
		return Result{}
	}
	*budget -= 1
	data, redirect, err := d.fetch(ctx, target, microsoftRequest(email))
	if err != nil {
		return Result{}
	}
	if redirect != "" {
		return d.microsoftAt(ctx, redirect, email, redirects-1, budget)
	}
	var doc microsoftDocument
	if !decodeDocument(data, &doc) {
		return Result{}
	}
	switch strings.ToLower(strings.TrimSpace(doc.Account.Action)) {
	case "redirecturl":
		return d.microsoftAt(ctx, strings.TrimSpace(doc.Account.RedirectURL), email, redirects-1, budget)
	case "redirectaddr":
		nextEmail, _, domain, err := emailParts(doc.Account.RedirectAddress)
		if err != nil || redirects == 0 {
			return Result{}
		}
		return d.microsoftDomain(ctx, nextEmail, domain, redirects-1, budget)
	case "settings":
		return microsoftSettings(doc, email)
	default:
		return Result{}
	}
}

func microsoftSettings(doc microsoftDocument, email string) Result {
	var out Result
	for _, protocol := range doc.Account.Protocols {
		kind := strings.ToLower(strings.TrimSpace(protocol.Type))
		if kind != "imap" && kind != "smtp" {
			continue
		}
		// Integrated Windows authentication is not supported by the clients.
		if strings.EqualFold(strings.TrimSpace(protocol.SPA), "on") {
			continue
		}
		if auth := strings.ToLower(strings.TrimSpace(protocol.AuthPackage)); auth != "" && auth != "basic" {
			continue
		}
		security := ""
		switch strings.ToLower(strings.TrimSpace(protocol.Encryption)) {
		case "ssl":
			security = "tls"
		case "tls", "starttls":
			security = "starttls"
		case "":
			if strings.EqualFold(strings.TrimSpace(protocol.SSL), "on") {
				security = "tls"
				if kind == "smtp" && protocol.Port != 465 {
					security = "starttls"
				}
			}
		}
		username := strings.TrimSpace(protocol.LoginName)
		if username == "" {
			username = email
		}
		server := supportedServer(kind, protocol.Server, protocol.Port, username, security, "Microsoft Autodiscover")
		if kind == "imap" && out.IMAP == nil {
			out.IMAP = server
		}
		if kind == "smtp" && out.SMTP == nil {
			out.SMTP = server
		}
	}
	return out
}

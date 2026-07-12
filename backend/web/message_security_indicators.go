// File overview: Conservative header-reported authentication and phishing indicators for displayed messages.

package web

import (
	"encoding/xml"
	"io"
	"net"
	"net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"rolltop/backend/store"
)

const (
	maxAuthenticationHeaderValues = 8
	maxAuthenticationHeaderBytes  = 16 * 1024
	maxSecurityHTMLBytes          = 512 * 1024
	maxSecurityHTMLTokens         = 50_000
	maxSecurityAnchors            = 128
	maxSecurityAnchorTextBytes    = 512
	maxSecurityHrefBytes          = 2 * 1024
	maxMessageSecuritySignals     = 12
)

var (
	authenticationResultClauseRE = regexp.MustCompile(`(?i)^(spf|dkim|dmarc)[\t ]*=[\t ]*([a-z][a-z0-9_-]{0,31})(?:[\t (]|$)`)
	receivedSPFResultRE          = regexp.MustCompile(`(?i)^[\t ]*(pass|fail|softfail|neutral|none|temperror|permerror)(?:[\t (;]|$)`)
	displayEmailRE               = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_{}|~-]+@([a-z0-9-]+(?:\.[a-z0-9-]+)+)`)
)

type reportedAuthenticationResult struct {
	Result string
	Source string
}

type reportedAuthentication struct {
	SPF   *reportedAuthenticationResult
	DKIM  *reportedAuthenticationResult
	DMARC *reportedAuthenticationResult
}

type messageSecuritySignal struct {
	Kind        string
	DisplayHost string
	TargetHost  string
	Scheme      string
}

type messageSecurityIndicators struct {
	ReportedAuthentication reportedAuthentication
	Signals                []messageSecuritySignal
}

func messageSecurityIndicatorsFor(header mail.Header, msg store.MessageRecord, bodyHTML string) messageSecurityIndicators {
	indicators := messageSecurityIndicators{
		ReportedAuthentication: reportedAuthenticationFromHeader(header),
	}
	indicators.Signals = appendSecuritySignals(indicators.Signals, senderSecuritySignals(header, msg)...)
	indicators.Signals = appendSecuritySignals(indicators.Signals, linkSecuritySignals(bodyHTML)...)
	return indicators
}

func reportedAuthenticationFromHeader(header mail.Header) reportedAuthentication {
	var reported reportedAuthentication
	values := messageHeaderValues(header, "Authentication-Results")
	if len(values) > maxAuthenticationHeaderValues {
		values = values[:maxAuthenticationHeaderValues]
	}
	for _, value := range values {
		value = limitSecurityString(value, maxAuthenticationHeaderBytes)
		candidate := reportedAuthentication{}
		clauses := strings.Split(value, ";")
		if len(clauses) > 64 {
			clauses = clauses[:64]
		}
		for _, clause := range clauses[1:] {
			match := authenticationResultClauseRE.FindStringSubmatch(strings.TrimSpace(clause))
			if len(match) != 3 {
				continue
			}
			method := strings.ToLower(match[1])
			result := normalizeReportedAuthenticationResult(method, match[2])
			if result == "" {
				continue
			}
			entry := &reportedAuthenticationResult{Result: result, Source: "authentication-results"}
			switch method {
			case "spf":
				if candidate.SPF == nil {
					candidate.SPF = entry
				}
			case "dkim":
				if candidate.DKIM == nil {
					candidate.DKIM = entry
				}
			case "dmarc":
				if candidate.DMARC == nil {
					candidate.DMARC = entry
				}
			}
		}
		if candidate.SPF != nil || candidate.DKIM != nil || candidate.DMARC != nil {
			reported = candidate
			break
		}
	}
	if reported.SPF == nil {
		values = messageHeaderValues(header, "Received-SPF")
		if len(values) > maxAuthenticationHeaderValues {
			values = values[:maxAuthenticationHeaderValues]
		}
		for _, value := range values {
			match := receivedSPFResultRE.FindStringSubmatch(limitSecurityString(value, maxAuthenticationHeaderBytes))
			if len(match) != 2 {
				continue
			}
			result := normalizeReportedAuthenticationResult("spf", match[1])
			if result != "" {
				reported.SPF = &reportedAuthenticationResult{Result: result, Source: "received-spf"}
				break
			}
		}
	}
	return reported
}

func messageHeaderValues(header mail.Header, name string) []string {
	if len(header) == 0 {
		return nil
	}
	return header[textproto.CanonicalMIMEHeaderKey(name)]
}

func normalizeReportedAuthenticationResult(method, result string) string {
	result = strings.ToLower(strings.TrimSpace(result))
	allowed := map[string]bool{
		"pass": true, "fail": true, "neutral": true, "none": true,
		"temperror": true, "permerror": true,
	}
	if method == "spf" {
		allowed["softfail"] = true
	}
	if method == "dkim" {
		allowed["policy"] = true
	}
	if method == "dmarc" {
		allowed["bestguesspass"] = true
	}
	if !allowed[result] {
		return ""
	}
	return result
}

func senderSecuritySignals(header mail.Header, msg store.MessageRecord) []messageSecuritySignal {
	from, ok := singleMessageAddress(msg.FromAddr)
	if !ok {
		from, ok = singleMessageAddress(header.Get("From"))
	}
	if !ok {
		return nil
	}
	fromDomain := securityEmailDomain(from.Address)
	if fromDomain == "" {
		return nil
	}
	var signals []messageSecuritySignal
	if match := displayEmailRE.FindStringSubmatch(from.Name); len(match) == 2 {
		displayDomain := normalizeSecurityHost(match[1])
		if displayDomain != "" && !sameSecurityDomain(displayDomain, fromDomain) {
			signals = append(signals, messageSecuritySignal{
				Kind: "sender_display_address_mismatch", DisplayHost: displayDomain, TargetHost: fromDomain,
			})
		}
	}
	if replyTo, ok := singleMessageAddress(header.Get("Reply-To")); ok {
		replyDomain := securityEmailDomain(replyTo.Address)
		if replyDomain != "" && !sameSecurityDomain(fromDomain, replyDomain) {
			signals = append(signals, messageSecuritySignal{
				Kind: "reply_to_domain_mismatch", DisplayHost: fromDomain, TargetHost: replyDomain,
			})
		}
	}
	return signals
}

func singleMessageAddress(value string) (*mail.Address, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	addresses, err := mail.ParseAddressList(value)
	if err != nil || len(addresses) != 1 || addresses[0] == nil {
		return nil, false
	}
	return addresses[0], true
}

func securityEmailDomain(address string) string {
	at := strings.LastIndex(strings.TrimSpace(address), "@")
	if at < 1 || at == len(address)-1 {
		return ""
	}
	return normalizeSecurityHost(address[at+1:])
}

type parsedSecurityAnchor struct {
	href        string
	text        strings.Builder
	depth       int
	hiddenDepth int
}

func linkSecuritySignals(bodyHTML string) []messageSecuritySignal {
	if strings.TrimSpace(bodyHTML) == "" {
		return nil
	}
	decoder := xml.NewDecoder(io.LimitReader(strings.NewReader(bodyHTML), maxSecurityHTMLBytes))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	var signals []messageSecuritySignal
	var active *parsedSecurityAnchor
	anchors := 0
	for tokens := 0; tokens < maxSecurityHTMLTokens && anchors < maxSecurityAnchors && len(signals) < maxMessageSecuritySignals; tokens++ {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(value.Name.Local)
			if active != nil {
				active.depth++
				if securityHiddenHTMLTag(name) {
					active.hiddenDepth++
				}
				continue
			}
			if name != "a" {
				continue
			}
			href := ""
			for _, attr := range value.Attr {
				if strings.EqualFold(attr.Name.Local, "href") {
					href = limitSecurityString(strings.TrimSpace(attr.Value), maxSecurityHrefBytes+1)
					break
				}
			}
			active = &parsedSecurityAnchor{href: href}
		case xml.CharData:
			if active == nil || active.hiddenDepth > 0 || active.text.Len() >= maxSecurityAnchorTextBytes {
				continue
			}
			remaining := maxSecurityAnchorTextBytes - active.text.Len()
			active.text.WriteString(limitSecurityString(string(value), remaining))
		case xml.EndElement:
			if active == nil {
				continue
			}
			name := strings.ToLower(value.Name.Local)
			if active.depth > 0 {
				if securityHiddenHTMLTag(name) && active.hiddenDepth > 0 {
					active.hiddenDepth--
				}
				active.depth--
				continue
			}
			if name == "a" {
				anchors++
				signals = appendSecuritySignals(signals, securitySignalsForLink(active.href, active.text.String())...)
				active = nil
			}
		}
	}
	if active != nil && len(signals) < maxMessageSecuritySignals {
		signals = appendSecuritySignals(signals, securitySignalsForLink(active.href, active.text.String())...)
	}
	return signals
}

func securityHiddenHTMLTag(name string) bool {
	switch name {
	case "script", "style", "title", "head", "template", "noscript":
		return true
	default:
		return false
	}
}

func securitySignalsForLink(rawHref, displayText string) []messageSecuritySignal {
	if rawHref == "" || len(rawHref) > maxSecurityHrefBytes {
		return nil
	}
	var signals []messageSecuritySignal
	scheme := explicitSecurityLinkScheme(rawHref)
	if riskySecurityLinkScheme(scheme) {
		signals = append(signals, messageSecuritySignal{Kind: "risky_link_scheme", Scheme: scheme})
	}
	if scheme != "" && scheme != "http" && scheme != "https" {
		return signals
	}
	target, err := url.Parse(strings.TrimSpace(rawHref))
	if err != nil || target.Hostname() == "" {
		return signals
	}
	displayHost := displayedSecurityLinkHost(displayText)
	targetHost := normalizeSecurityHost(target.Hostname())
	if displayHost != "" && targetHost != "" && !sameSecurityDomain(displayHost, targetHost) {
		signals = append(signals, messageSecuritySignal{
			Kind: "link_destination_mismatch", DisplayHost: displayHost, TargetHost: targetHost,
		})
	}
	return signals
}

func explicitSecurityLinkScheme(value string) string {
	value = strings.TrimSpace(value)
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon > 32 {
		return ""
	}
	for _, delimiter := range []byte{'/', '?', '#'} {
		if idx := strings.IndexByte(value, delimiter); idx >= 0 && idx < colon {
			return ""
		}
	}
	prefix := strings.Map(func(r rune) rune {
		if r <= unicode.MaxASCII && (unicode.IsControl(r) || unicode.IsSpace(r)) {
			return -1
		}
		return unicode.ToLower(r)
	}, value[:colon])
	if prefix == "" || prefix[0] < 'a' || prefix[0] > 'z' {
		return ""
	}
	for _, r := range prefix[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '+' && r != '-' && r != '.' {
			return ""
		}
	}
	return prefix
}

func riskySecurityLinkScheme(scheme string) bool {
	switch scheme {
	case "javascript", "vbscript", "data", "file", "intent", "content":
		return true
	default:
		return false
	}
}

func displayedSecurityLinkHost(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(value))
	if value == "" || len(value) > 512 || len(strings.Fields(value)) != 1 || strings.Contains(value, "@") {
		return ""
	}
	value = strings.Trim(value, `<>[](){}"'.,;!?`)
	if value == "" {
		return ""
	}
	candidate := value
	if strings.HasPrefix(candidate, "//") {
		candidate = "https:" + candidate
	} else if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return ""
	}
	return normalizeSecurityHost(parsed.Hostname())
}

func normalizeSecurityHost(value string) string {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	value = strings.Trim(value, "[]")
	if value == "" || len(value) > 253 {
		return ""
	}
	for _, r := range value {
		if r > unicode.MaxASCII {
			return ""
		}
	}
	if ip := net.ParseIP(value); ip != nil {
		return strings.ToLower(ip.String())
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return ""
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return ""
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return ""
			}
		}
	}
	return value
}

func sameSecurityDomain(left, right string) bool {
	left = normalizeSecurityHost(left)
	right = normalizeSecurityHost(right)
	if left == "" || right == "" {
		return true
	}
	// Exact host comparison avoids treating unrelated tenants on a shared public
	// or private suffix (for example github.io) as the same identity. The UI
	// labels these differences as cautions because sibling subdomains can still
	// be legitimate.
	return left == right
}

func appendSecuritySignals(existing []messageSecuritySignal, next ...messageSecuritySignal) []messageSecuritySignal {
	if len(existing) >= maxMessageSecuritySignals {
		return existing
	}
	seen := make(map[string]bool, len(existing)+len(next))
	for _, signal := range existing {
		seen[securitySignalKey(signal)] = true
	}
	for _, signal := range next {
		if signal.Kind == "" {
			continue
		}
		key := securitySignalKey(signal)
		if seen[key] {
			continue
		}
		seen[key] = true
		existing = append(existing, signal)
		if len(existing) >= maxMessageSecuritySignals {
			break
		}
	}
	return existing
}

func securitySignalKey(signal messageSecuritySignal) string {
	return signal.Kind + "\x00" + signal.DisplayHost + "\x00" + signal.TargetHost + "\x00" + signal.Scheme
}

func limitSecurityString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}

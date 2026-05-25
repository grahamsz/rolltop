// File overview: MIME parsing for message headers, bodies, attachments, and inline parts.

package mailparse

import (
	"bytes"
	"encoding/base64"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

var htmlTextNoiseBlockRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>|<head\b[^>]*>.*?</head>|<title\b[^>]*>.*?</title>|<!--.*?-->`)

type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	IsInline    bool
	Data        []byte
}

func (a Attachment) SearchableText() string {
	mediaType, _, err := mime.ParseMediaType(a.ContentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(a.ContentType))
	}
	mediaType = strings.ToLower(mediaType)
	ext := strings.ToLower(filepath.Ext(a.Filename))
	if mediaType == "text/html" || ext == ".html" || ext == ".htm" {
		return normalizeText(stripHTML(string(limitBytes(a.Data, 512*1024))))
	}
	if isSearchableTextType(mediaType, ext) {
		return normalizeText(string(limitBytes(a.Data, 512*1024)))
	}
	return ""
}

type ParsedMessage struct {
	MessageID  string
	InReplyTo  string
	References string
	Subject    string
	From       string
	To         string
	CC         string
	Date       time.Time
	Text       string
	HTML       string
	Files      []Attachment
}

func Parse(raw []byte) (ParsedMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ParsedMessage{}, err
	}
	decoder := wordDecoder()
	subject, _ := decoder.DecodeHeader(msg.Header.Get("Subject"))
	parsed := ParsedMessage{
		MessageID:  strings.TrimSpace(msg.Header.Get("Message-ID")),
		InReplyTo:  strings.TrimSpace(msg.Header.Get("In-Reply-To")),
		References: strings.TrimSpace(msg.Header.Get("References")),
		Subject:    strings.TrimSpace(subject),
		From:       addressHeader(msg.Header.Get("From")),
		To:         addressHeader(msg.Header.Get("To")),
		CC:         addressHeader(msg.Header.Get("Cc")),
	}
	if d, err := mail.ParseDate(msg.Header.Get("Date")); err == nil {
		parsed.Date = d.UTC()
	}
	if err := parsePart(textproto.MIMEHeader(msg.Header), msg.Body, &parsed); err != nil {
		if isTolerableEOF(err) {
			parsed.Text = cleanIndexedText(parsed.Text)
			return parsed, nil
		}
		return ParsedMessage{}, err
	}
	parsed.Text = cleanIndexedText(parsed.Text)
	return parsed, nil
}

func ParseDisplayBody(r io.Reader) (string, string, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return "", "", err
	}
	var parsed ParsedMessage
	if err := parseDisplayPart(textproto.MIMEHeader(msg.Header), msg.Body, &parsed); err != nil {
		if isTolerableEOF(err) {
			return normalizeDisplayText(parsed.Text), parsed.HTML, nil
		}
		return "", "", err
	}
	return normalizeDisplayText(parsed.Text), parsed.HTML, nil
}

func parsePart(header textproto.MIMEHeader, body io.Reader, parsed *ParsedMessage) error {
	contentType := header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if isTolerableEOF(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := parsePart(part.Header, part, parsed); err != nil {
				if isTolerableEOF(err) {
					return nil
				}
				return err
			}
		}
	}

	decoded, err := io.ReadAll(decodeTransfer(header, body))
	if err != nil {
		if isTolerableEOF(err) {
			decoded = bytes.TrimSpace(decoded)
		} else {
			return err
		}
	}
	disposition, dispParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := ""
	if dispParams != nil {
		filename = dispParams["filename"]
	}
	if filename == "" && params != nil {
		filename = params["name"]
	}
	contentID := strings.Trim(header.Get("Content-ID"), "<>")
	if filename != "" || strings.EqualFold(disposition, "attachment") {
		parsed.Files = append(parsed.Files, Attachment{
			Filename:    decodedHeader(filename),
			ContentType: mediaType,
			ContentID:   contentID,
			IsInline:    isInlineMIMEFile(disposition, mediaType, contentID),
			Data:        decoded,
		})
		return nil
	}

	switch strings.ToLower(mediaType) {
	case "text/plain":
		parsed.Text += "\n" + decodeTextBytes(decoded, params["charset"])
	case "text/html":
		htmlText := decodeTextBytes(decoded, params["charset"])
		if strings.TrimSpace(parsed.HTML) == "" {
			parsed.HTML = htmlText
		}
		if strings.TrimSpace(parsed.Text) == "" {
			parsed.Text += "\n" + stripHTML(htmlText)
		}
	}
	return nil
}

func isInlineMIMEFile(disposition, mediaType, contentID string) bool {
	disposition = strings.ToLower(strings.TrimSpace(disposition))
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	contentID = strings.TrimSpace(contentID)
	if disposition == "attachment" {
		return false
	}
	if disposition == "inline" {
		return true
	}
	return contentID != "" && strings.HasPrefix(mediaType, "image/")
}

func parseDisplayPart(header textproto.MIMEHeader, body io.Reader, parsed *ParsedMessage) error {
	contentType := header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if isTolerableEOF(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := parseDisplayPart(part.Header, part, parsed); err != nil {
				if isTolerableEOF(err) {
					return nil
				}
				return err
			}
		}
	}

	disposition, dispParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := ""
	if dispParams != nil {
		filename = dispParams["filename"]
	}
	if filename == "" && params != nil {
		filename = params["name"]
	}
	if filename != "" || strings.EqualFold(disposition, "attachment") {
		_, _ = io.Copy(io.Discard, decodeTransfer(header, body))
		return nil
	}

	decoded, err := io.ReadAll(decodeTransfer(header, body))
	if err != nil {
		if isTolerableEOF(err) {
			decoded = bytes.TrimSpace(decoded)
		} else {
			return err
		}
	}
	switch strings.ToLower(mediaType) {
	case "text/plain":
		parsed.Text += "\n" + decodeTextBytes(decoded, params["charset"])
	case "text/html":
		htmlText := decodeTextBytes(decoded, params["charset"])
		if strings.TrimSpace(parsed.HTML) == "" {
			parsed.HTML = htmlText
		}
		if strings.TrimSpace(parsed.Text) == "" {
			parsed.Text += "\n" + stripHTML(htmlText)
		}
	}
	return nil
}

func isTolerableEOF(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") || strings.Contains(msg, "nextpart: eof")
}

func decodeTransfer(header textproto.MIMEHeader, body io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

func addressHeader(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	addrs, err := (&mail.AddressParser{WordDecoder: wordDecoder()}).ParseList(value)
	if err != nil {
		return strings.TrimSpace(value)
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.Name != "" {
			out = append(out, strconv.Quote(strings.TrimSpace(addr.Name))+" <"+addr.Address+">")
		} else {
			out = append(out, addr.Address)
		}
	}
	return strings.Join(out, ", ")
}

func decodedHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	out, err := wordDecoder().DecodeHeader(value)
	if err != nil {
		return value
	}
	return out
}

func DecodeTextBytes(data []byte, charset string) string {
	return decodeTextBytes(data, charset)
}

func wordDecoder() *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: charsetReader}
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	enc, err := lookupTextEncoding(charset)
	if err != nil {
		return nil, err
	}
	if enc == nil {
		return input, nil
	}
	return transform.NewReader(input, enc.NewDecoder()), nil
}

func decodeTextBytes(data []byte, charset string) string {
	if strings.TrimSpace(charset) != "" {
		if text, ok := decodeBytesAsCharset(data, charset); ok {
			return text
		}
	}
	if looksLikeISO2022JP(data) {
		if text, ok := decodeBytesAsCharset(data, "iso-2022-jp"); ok {
			return text
		}
	}
	if utf8.Valid(data) {
		return string(data)
	}
	return string(data)
}

func decodeBytesAsCharset(data []byte, charset string) (string, bool) {
	enc, err := lookupTextEncoding(charset)
	if err != nil {
		return "", false
	}
	if enc == nil {
		return string(data), true
	}
	out, err := io.ReadAll(transform.NewReader(bytes.NewReader(data), enc.NewDecoder()))
	if err != nil {
		return "", false
	}
	return string(out), true
}

func lookupTextEncoding(charset string) (encoding.Encoding, error) {
	charset = strings.ToLower(strings.Trim(strings.TrimSpace(charset), `"`))
	switch charset {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return nil, nil
	default:
		return ianaindex.MIME.Encoding(charset)
	}
}

func looksLikeISO2022JP(data []byte) bool {
	return bytes.Contains(data, []byte("\x1b$B")) ||
		bytes.Contains(data, []byte("\x1b$@")) ||
		bytes.Contains(data, []byte("\x1b(J"))
}

func normalizeText(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func normalizeDisplayText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func cleanIndexedText(value string) string {
	value = removeIndexedCSSRules(value)
	value = trimIndexedTextJunk(value)
	return normalizeDisplayText(value)
}

func removeIndexedCSSRules(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		openRel := strings.Index(value[i:], "{")
		if openRel < 0 {
			b.WriteString(value[i:])
			break
		}
		open := i + openRel
		close := indexedCSSRuleClose(value, open)
		if close < 0 {
			b.WriteString(value[i:])
			break
		}
		start := indexedCSSSelectorStart(value, open)
		selector := strings.TrimSpace(value[start:open])
		body := strings.TrimSpace(value[open+1 : close])
		if looksLikeIndexedCSSRule(selector, body) {
			b.WriteString(value[i:start])
			b.WriteByte(' ')
			i = close + 1
			continue
		}
		b.WriteString(value[i : open+1])
		i = open + 1
	}
	return b.String()
}

func indexedCSSRuleClose(value string, open int) int {
	depth := 0
	for i := open; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i += size
	}
	return -1
}

func indexedCSSSelectorStart(value string, open int) int {
	start := open
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(value[:start])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if r == '}' || r == ';' || r == '\n' || r == '\r' {
			break
		}
		start -= size
	}
	for start < open {
		r, size := utf8.DecodeRuneInString(value[start:open])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	return start
}

func looksLikeIndexedCSSRule(selector, body string) bool {
	if selector == "" || body == "" || len(body) > 2000 {
		return false
	}
	lowerSelector := strings.ToLower(selector)
	lowerBody := strings.ToLower(body)
	if !strings.Contains(lowerBody, ":") {
		return false
	}
	for _, token := range []string{"margin", "padding", "color", "font", "display", "width", "height", "box-sizing", "line-height", "text-decoration", "background", "border"} {
		if strings.Contains(lowerBody, token+":") || strings.Contains(lowerBody, token+"-") {
			return true
		}
	}
	return strings.ContainsAny(lowerSelector, "#.*>[],:") ||
		strings.Contains(lowerSelector, "body") ||
		strings.Contains(lowerSelector, "table") ||
		strings.Contains(lowerSelector, "div") ||
		strings.Contains(lowerSelector, "span") ||
		strings.Contains(lowerSelector, "img")
}

func trimIndexedTextJunk(value string) string {
	value = strings.TrimSpace(value)
	for {
		next := strings.TrimLeft(value, " -_.,;:|}")
		if next == value {
			return value
		}
		value = strings.TrimSpace(next)
	}
}

func limitBytes(data []byte, max int) []byte {
	if len(data) <= max {
		return data
	}
	return data[:max]
}

func isSearchableTextType(mediaType, ext string) bool {
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/xhtml+xml", "application/csv", "application/ics", "application/javascript", "application/x-javascript":
		return true
	}
	switch ext {
	case ".txt", ".text", ".md", ".markdown", ".csv", ".tsv", ".json", ".xml", ".html", ".htm", ".ics", ".vcf", ".log", ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".css", ".sql", ".yaml", ".yml", ".toml":
		return true
	default:
		return false
	}
}

func stripHTML(value string) string {
	value = html.UnescapeString(value)
	value = htmlTextNoiseBlockRE.ReplaceAllString(value, " ")
	var b strings.Builder
	inTag := false
	for _, r := range value {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
			b.WriteByte(' ')
		default:
			if !inTag {
				if unicode.IsSpace(r) {
					b.WriteByte(' ')
				} else {
					b.WriteRune(r)
				}
			}
		}
	}
	return html.UnescapeString(b.String())
}

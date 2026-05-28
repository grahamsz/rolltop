// File overview: MIME parsing for message headers, bodies, attachments, and inline parts.

package mailparse

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
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

const (
	maxAttachmentSearchBytes = 512 * 1024
	maxPDFAttachmentBytes    = 32 * 1024 * 1024
	maxPDFSearchTextBytes    = 1024 * 1024
	maxOfficeAttachmentBytes = 32 * 1024 * 1024
	maxOfficeSearchTextBytes = 1024 * 1024
	pdfExtractionTimeout     = 10 * time.Second
	officeExtractionTimeout  = 10 * time.Second
)

var (
	pdfTextExtractor = extractPDFTextWithPdftotext
	docTextExtractor = extractDOCTextWithExternalTool
)

// Attachment is a decoded MIME part that may be indexed, displayed, or downloaded.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	IsInline    bool
	Data        []byte
}

// SearchableText extracts bounded text from attachments that are safe and useful
// to index. Binary files return an empty string so attachment bodies are not kept
// as separate blobs just for search.
func (a Attachment) SearchableText() string {
	mediaType, _, err := mime.ParseMediaType(a.ContentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(a.ContentType))
	}
	mediaType = strings.ToLower(mediaType)
	ext := strings.ToLower(filepath.Ext(a.Filename))
	if mediaType == "text/html" || ext == ".html" || ext == ".htm" {
		return normalizeText(stripHTML(string(limitBytes(a.Data, maxAttachmentSearchBytes))))
	}
	if isSearchablePDFType(mediaType, ext) {
		text, err := pdfTextExtractor(a.Data)
		if err != nil {
			return ""
		}
		return normalizeText(string(limitBytes([]byte(text), maxPDFSearchTextBytes)))
	}
	if isSearchableDOCXType(mediaType, ext) {
		text, err := extractDOCXText(a.Data)
		if err != nil {
			return ""
		}
		return normalizeText(string(limitBytes([]byte(text), maxOfficeSearchTextBytes)))
	}
	if isSearchableODSType(mediaType, ext) {
		text, err := extractODSText(a.Data)
		if err != nil {
			return ""
		}
		return normalizeText(string(limitBytes([]byte(text), maxOfficeSearchTextBytes)))
	}
	if isSearchableDOCType(mediaType, ext) {
		text, err := docTextExtractor(a.Data)
		if err != nil {
			return ""
		}
		return normalizeText(string(limitBytes([]byte(text), maxOfficeSearchTextBytes)))
	}
	if isSearchableTextType(mediaType, ext) {
		return normalizeText(string(limitBytes(a.Data, maxAttachmentSearchBytes)))
	}
	return ""
}

// ParsedMessage is the normalized output from parsing a raw RFC822 message.
type ParsedMessage struct {
	MessageID   string
	InReplyTo   string
	References  string
	Subject     string
	From        string
	To          string
	CC          string
	Date        time.Time
	Text        string
	HTML        string
	Files       []Attachment
	IsEncrypted bool
	IsSigned    bool
}

// Parse is the indexing/parser entrypoint. It decodes headers, walks MIME parts,
// collects text/html bodies and attachment metadata/data, and normalizes indexed
// text so CSS-heavy marketing mail does not poison snippets or search.
func Parse(raw []byte) (ParsedMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ParsedMessage{}, err
	}
	decoder := wordDecoder()
	subject, _ := decoder.DecodeHeader(msg.Header.Get("Subject"))
	pgpEncrypted, pgpSigned := DetectPGP(raw)
	parsed := ParsedMessage{
		IsEncrypted: pgpEncrypted,
		IsSigned:    pgpSigned,
		MessageID:   strings.TrimSpace(msg.Header.Get("Message-ID")),
		InReplyTo:   strings.TrimSpace(msg.Header.Get("In-Reply-To")),
		References:  strings.TrimSpace(msg.Header.Get("References")),
		Subject:     strings.TrimSpace(subject),
		From:        addressHeader(msg.Header.Get("From")),
		To:          addressHeader(msg.Header.Get("To")),
		CC:          addressHeader(msg.Header.Get("Cc")),
	}
	if d, err := mail.ParseDate(msg.Header.Get("Date")); err == nil {
		parsed.Date = d.UTC()
	}
	if err := parsePart(textproto.MIMEHeader(msg.Header), msg.Body, &parsed); err != nil {
		if isTolerableEOF(err) {
			if parsed.IsEncrypted {
				parsed.Text = ""
				parsed.HTML = ""
				parsed.Files = nil
			} else {
				if parsed.IsSigned {
					stripPGPSignedParsedBody(&parsed)
				}
				parsed.Text = cleanIndexedText(parsed.Text)
			}
			return parsed, nil
		}
		return ParsedMessage{}, err
	}
	if parsed.IsEncrypted {
		parsed.Text = ""
		parsed.HTML = ""
		parsed.Files = nil
	} else {
		if parsed.IsSigned {
			stripPGPSignedParsedBody(&parsed)
		}
		parsed.Text = cleanIndexedText(parsed.Text)
	}
	return parsed, nil
}

// ParseDisplayBody is the lighter display path used when a raw message is loaded
// on demand. It skips attachment bodies and returns body text/html only.
func ParseDisplayBody(r io.Reader) (string, string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", "", err
	}
	encrypted, signed := DetectPGP(raw)
	if encrypted {
		return "", "", nil
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}
	var parsed ParsedMessage
	if err := parseDisplayPart(textproto.MIMEHeader(msg.Header), msg.Body, &parsed); err != nil {
		if isTolerableEOF(err) {
			text, html := pgpDisplayBody(parsed.Text, parsed.HTML, signed)
			return text, html, nil
		}
		return "", "", err
	}
	text, html := pgpDisplayBody(parsed.Text, parsed.HTML, signed)
	return text, html, nil
}

// parsePart recursively walks the MIME tree for indexing. Attachments keep their
// decoded data long enough for search extraction; text/html parts feed the message
// body fields used for search and previews.
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

// parseDisplayPart mirrors parsePart but discards attachment streams immediately,
// avoiding unnecessary memory use when the caller only needs renderable body text.
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

// DecodeTextBytes exposes MIME charset decoding for callers that need consistent text handling outside full parsing.
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

// decodeTextBytes prefers the declared MIME charset, then handles common older
// Japanese escape sequences, and finally falls back to UTF-8/raw bytes so bad mail
// still produces some display/index text.
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

func stripPGPSignedParsedBody(parsed *ParsedMessage) {
	clear, ok := stripInlinePGPSignedText(parsed.Text)
	if !ok {
		return
	}
	parsed.Text = clear
	parsed.HTML = ""
}

func pgpDisplayBody(text, htmlBody string, signed bool) (string, string) {
	text = normalizeDisplayText(text)
	if signed {
		if clear, ok := stripInlinePGPSignedText(text); ok {
			return normalizeDisplayText(clear), ""
		}
	}
	return text, htmlBody
}

func stripInlinePGPSignedText(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	begin := strings.Index(value, "-----BEGIN PGP SIGNED MESSAGE-----")
	if begin < 0 {
		return "", false
	}
	sigBeginRel := strings.Index(value[begin:], "-----BEGIN PGP SIGNATURE-----")
	if sigBeginRel < 0 {
		return "", false
	}
	sigBegin := begin + sigBeginRel
	bodyOffset := clearSignedBodyOffset(value[begin:sigBegin])
	if bodyOffset < 0 {
		return "", false
	}
	body := value[begin+bodyOffset : sigBegin]
	body = unescapeClearSignedBody(body)

	replacement := normalizeDisplayText(body)
	prefix := strings.TrimSpace(value[:begin])
	suffix := ""
	if sigEndRel := strings.Index(value[sigBegin:], "-----END PGP SIGNATURE-----"); sigEndRel >= 0 {
		suffixStart := sigBegin + sigEndRel + len("-----END PGP SIGNATURE-----")
		suffix = strings.TrimSpace(value[suffixStart:])
	}

	parts := make([]string, 0, 3)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if replacement != "" {
		parts = append(parts, replacement)
	}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return normalizeDisplayText(strings.Join(parts, "\n\n")), true
}

func clearSignedBodyOffset(block string) int {
	lineEnd := strings.IndexByte(block, '\n')
	if lineEnd < 0 {
		return -1
	}
	pos := lineEnd + 1
	for pos <= len(block) {
		next := strings.IndexByte(block[pos:], '\n')
		lineEnd = len(block)
		lineNext := len(block)
		if next >= 0 {
			lineEnd = pos + next
			lineNext = lineEnd + 1
		}
		if strings.TrimSpace(block[pos:lineEnd]) == "" {
			return lineNext
		}
		if next < 0 {
			return -1
		}
		pos = lineNext
	}
	return -1
}

func unescapeClearSignedBody(value string) string {
	value = strings.Trim(value, "\n")
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "- ") {
			lines[i] = strings.TrimPrefix(line, "- ")
		}
	}
	return strings.Join(lines, "\n")
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

// cleanIndexedText removes CSS-looking debris before body text is stored/indexed;
// display-time rendering should not be responsible for fixing indexed snippets.
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
			if start > i {
				b.WriteString(value[i:start])
			}
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
		if r == '{' || r == '}' || r == ';' || r == '\n' || r == '\r' {
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

func isSearchablePDFType(mediaType, ext string) bool {
	return mediaType == "application/pdf" || mediaType == "application/x-pdf" || ext == ".pdf"
}

func isSearchableDOCXType(mediaType, ext string) bool {
	return mediaType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || ext == ".docx"
}

func isSearchableDOCType(mediaType, ext string) bool {
	switch mediaType {
	case "application/msword", "application/vnd.ms-word", "application/x-msword":
		return true
	default:
		return ext == ".doc"
	}
}

func isSearchableODSType(mediaType, ext string) bool {
	return mediaType == "application/vnd.oasis.opendocument.spreadsheet" || ext == ".ods"
}

func extractPDFTextWithPdftotext(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > maxPDFAttachmentBytes {
		return "", errors.New("pdf attachment too large for search extraction")
	}
	tmp, err := os.CreateTemp("", "rolltop-pdf-*.pdf")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfExtractionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pdftotext", "-enc", "UTF-8", "-layout", tmpName, "-")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return string(limitBytes(out, maxPDFSearchTextBytes)), nil
}

func extractDOCXText(data []byte) (string, error) {
	return extractOfficeZipText(data, isDOCXTextPart, map[string]bool{"t": true})
}

func extractODSText(data []byte) (string, error) {
	return extractOfficeZipText(data, func(name string) bool { return name == "content.xml" }, map[string]bool{"p": true, "span": true, "h": true, "a": true})
}

func extractOfficeZipText(data []byte, includePart func(string) bool, textElements map[string]bool) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > maxOfficeAttachmentBytes {
		return "", errors.New("office attachment too large for search extraction")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, file := range reader.File {
		name := strings.ToLower(file.Name)
		if !includePart(name) {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", err
		}
		err = appendOfficeXMLText(&out, io.LimitReader(rc, maxOfficeAttachmentBytes), textElements)
		closeErr := rc.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if out.Len() >= maxOfficeSearchTextBytes {
			break
		}
	}
	return out.String(), nil
}

func isDOCXTextPart(name string) bool {
	if name == "word/document.xml" {
		return true
	}
	if !strings.HasPrefix(name, "word/") || !strings.HasSuffix(name, ".xml") {
		return false
	}
	base := filepath.Base(name)
	return strings.HasPrefix(base, "header") || strings.HasPrefix(base, "footer") || base == "footnotes.xml" || base == "endnotes.xml" || base == "comments.xml"
}

func appendOfficeXMLText(out *strings.Builder, r io.Reader, textElements map[string]bool) error {
	decoder := xml.NewDecoder(r)
	decoder.Strict = false
	textDepth := 0
	for {
		tok, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if textDepth > 0 || textElements[strings.ToLower(t.Name.Local)] {
				textDepth++
			}
		case xml.CharData:
			if textDepth > 0 {
				appendOfficeText(out, string(t))
				if out.Len() >= maxOfficeSearchTextBytes {
					return nil
				}
			}
		case xml.EndElement:
			if textDepth > 0 {
				textDepth--
			}
		}
	}
}

func appendOfficeText(out *strings.Builder, value string) {
	value = strings.TrimSpace(value)
	if value == "" || out.Len() >= maxOfficeSearchTextBytes {
		return
	}
	remaining := maxOfficeSearchTextBytes - out.Len()
	if remaining <= 0 {
		return
	}
	if out.Len() > 0 {
		out.WriteByte(' ')
		remaining--
	}
	if len(value) > remaining {
		value = string(limitBytes([]byte(value), remaining))
	}
	out.WriteString(value)
}

func extractDOCTextWithExternalTool(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > maxOfficeAttachmentBytes {
		return "", errors.New("doc attachment too large for search extraction")
	}
	tmp, err := os.CreateTemp("", "rolltop-doc-*.doc")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	commands := [][]string{
		{"antiword", "-m", "UTF-8.txt", tmpName},
		{"catdoc", "-w", tmpName},
	}
	var lastErr error
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), officeExtractionTimeout)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stderr = io.Discard
		out, err := cmd.Output()
		if ctx.Err() != nil {
			cancel()
			return "", ctx.Err()
		}
		cancel()
		if err == nil {
			return string(limitBytes(out, maxOfficeSearchTextBytes)), nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("doc text extractor unavailable")
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

var (
	inlinePGPMessageRE = regexp.MustCompile(`(?is)-----BEGIN PGP MESSAGE-----.*?-----END PGP MESSAGE-----`)
	inlinePGPSignedRE  = regexp.MustCompile(`(?is)-----BEGIN PGP SIGNED MESSAGE-----.*?-----END PGP SIGNATURE-----`)
)

// DetectPGP reports whether a raw RFC822 message appears to contain OpenPGP
// encrypted or signed content. It intentionally treats this as metadata only;
// private-key operations happen in the browser.
func DetectPGP(raw []byte) (encrypted bool, signed bool) {
	lower := strings.ToLower(string(limitBytes(raw, 256*1024)))
	if strings.Contains(lower, "multipart/encrypted") || strings.Contains(lower, "application/pgp-encrypted") || inlinePGPMessageRE.Match(raw) {
		encrypted = true
	}
	if strings.Contains(lower, "multipart/signed") && strings.Contains(lower, "application/pgp-signature") {
		signed = true
	}
	if strings.Contains(lower, "application/pgp-signature") || inlinePGPSignedRE.Match(raw) {
		signed = true
	}
	return encrypted, signed
}

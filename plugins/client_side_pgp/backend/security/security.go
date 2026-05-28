package security

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"

	"rolltop/backend/plugins"
)

var (
	inlinePGPMessageRE = regexp.MustCompile(`(?is)-----BEGIN PGP MESSAGE-----.*?-----END PGP MESSAGE-----`)
	inlinePGPSignedRE  = regexp.MustCompile(`(?is)-----BEGIN PGP SIGNED MESSAGE-----.*?-----END PGP SIGNATURE-----`)
)

func Detect(raw []byte, body plugins.MessageBody) plugins.MessageSecurityState {
	encrypted, signed := detectPGP(raw, body.Text+"\n"+body.HTML)
	return plugins.MessageSecurityState{Encrypted: encrypted, Signed: signed}
}

func Transform(raw []byte, state plugins.MessageSecurityState, body plugins.MessageBody) plugins.MessageBodyTransform {
	if state.Encrypted {
		if body.Purpose == "display" {
			text := encryptedDisplayBody(raw)
			if strings.TrimSpace(text) == "" {
				text = body.Text
			}
			return plugins.MessageBodyTransform{
				Applied: true,
				Body:    plugins.MessageBody{Purpose: body.Purpose, Text: normalizeDisplayText(text)},
			}
		}
		return plugins.MessageBodyTransform{
			Applied:         true,
			Body:            plugins.MessageBody{Purpose: body.Purpose},
			DropAttachments: true,
		}
	}
	if state.Signed {
		if clear, ok := stripInlinePGPSignedText(body.Text); ok {
			return plugins.MessageBodyTransform{
				Applied: true,
				Body:    plugins.MessageBody{Purpose: body.Purpose, Text: clear},
			}
		}
	}
	return plugins.MessageBodyTransform{}
}

func detectPGP(raw []byte, fallback string) (encrypted bool, signed bool) {
	lower := strings.ToLower(string(limitSecurityBytes(raw, 256*1024)))
	if strings.TrimSpace(lower) == "" {
		lower = strings.ToLower(limitString(fallback, 256*1024))
	}
	if strings.Contains(lower, "multipart/encrypted") || strings.Contains(lower, "application/pgp-encrypted") || inlinePGPMessageRE.Match(raw) || inlinePGPMessageRE.MatchString(fallback) {
		encrypted = true
	}
	if strings.Contains(lower, "multipart/signed") && strings.Contains(lower, "application/pgp-signature") {
		signed = true
	}
	if strings.Contains(lower, "application/pgp-signature") || inlinePGPSignedRE.Match(raw) || inlinePGPSignedRE.MatchString(fallback) {
		signed = true
	}
	return encrypted, signed
}

func encryptedDisplayBody(raw []byte) string {
	if match := inlinePGPMessageRE.Find(raw); len(match) > 0 {
		return normalizeDisplayBytes(match)
	}
	if text := encryptedMIMEPayloadDisplay(raw); text != "" {
		return text
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return ""
	}
	return normalizeDisplayBytes(body)
}

func encryptedMIMEPayloadDisplay(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return encryptedMIMEPartDisplay(textproto.MIMEHeader(msg.Header), msg.Body)
}

func encryptedMIMEPartDisplay(header textproto.MIMEHeader, body io.Reader) string {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return ""
			}
			if err != nil {
				return ""
			}
			if text := encryptedMIMEPartDisplay(part.Header, part); text != "" {
				return text
			}
		}
	}
	if mediaType == "application/pgp-encrypted" {
		_, _ = io.Copy(io.Discard, body)
		return ""
	}
	decoded, err := io.ReadAll(body)
	if err != nil {
		return ""
	}
	if match := inlinePGPMessageRE.Find(decoded); len(match) > 0 {
		return normalizeDisplayBytes(match)
	}
	disposition, dispParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := ""
	if dispParams != nil {
		filename = dispParams["filename"]
	}
	if filename == "" && params != nil {
		filename = params["name"]
	}
	name := strings.ToLower(strings.TrimSpace(filename))
	if mediaType == "application/octet-stream" && strings.Contains(name, "encrypted") {
		return normalizeDisplayBytes(decoded)
	}
	if strings.EqualFold(disposition, "inline") && strings.HasSuffix(name, ".asc") {
		return normalizeDisplayBytes(decoded)
	}
	return ""
}

func normalizeDisplayBytes(value []byte) string {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return ""
	}
	return normalizeDisplayText(string(value))
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
	block := value[begin:sigBegin]
	bodyOffset := clearSignedBodyOffset(block)
	if bodyOffset < 0 {
		return "", false
	}
	prefix := strings.TrimSpace(value[:begin])
	replacement := unescapeClearSignedBody(block[bodyOffset:])
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

func limitSecurityBytes(value []byte, n int) []byte {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

func limitString(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

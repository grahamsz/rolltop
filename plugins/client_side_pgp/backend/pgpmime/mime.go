package pgpmime

import (
	"fmt"
	"mime"
	"strings"

	"rolltop/backend/plugins"
	"rolltop/backend/smtpclient"
)

func Override(body plugins.ComposeMessageBodyContext) (*plugins.MIMEBodyOverride, error) {
	if body.Metadata["pgp_mime"] != "true" {
		return nil, nil
	}
	if body.Metadata["pgp_encrypted"] == "true" {
		return encryptedOverride(body), nil
	}
	if body.Metadata["pgp_signed"] == "true" {
		return signedOverride(body), nil
	}
	return nil, nil
}

func encryptedOverride(body plugins.ComposeMessageBodyContext) *plugins.MIMEBodyOverride {
	boundary := smtpclient.MIMEBoundary(body.MessageID, "pgp-encrypted")
	var b strings.Builder
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writeMIMEHeader(&b, "Content-Type", "application/pgp-encrypted")
	writeMIMEHeader(&b, "Content-Description", "PGP/MIME version identification")
	b.WriteString("\r\nVersion: 1\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writeMIMEHeader(&b, "Content-Type", mime.FormatMediaType("application/octet-stream", map[string]string{"name": "encrypted.asc"}))
	writeMIMEHeader(&b, "Content-Description", "OpenPGP encrypted message")
	writeMIMEHeader(&b, "Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": "encrypted.asc"}))
	writeMIMEHeader(&b, "Content-Transfer-Encoding", "7bit")
	b.WriteString("\r\n")
	entity := normalizeMIMECRLF(body.BodyText)
	b.WriteString(entity)
	if !strings.HasSuffix(entity, "\r\n") {
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return &plugins.MIMEBodyOverride{
		ContentType: mime.FormatMediaType("multipart/encrypted", map[string]string{
			"protocol": "application/pgp-encrypted",
			"boundary": boundary,
		}),
		Body: b.String(),
	}
}

func signedOverride(body plugins.ComposeMessageBodyContext) *plugins.MIMEBodyOverride {
	boundary := smtpclient.MIMEBoundary(body.MessageID, "pgp-signed")
	var b strings.Builder
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	entity := normalizeMIMECRLF(body.BodyText)
	b.WriteString(entity)
	if !strings.HasSuffix(entity, "\r\n") {
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writeMIMEHeader(&b, "Content-Type", mime.FormatMediaType("application/pgp-signature", map[string]string{"name": "signature.asc"}))
	writeMIMEHeader(&b, "Content-Description", "OpenPGP digital signature")
	writeMIMEHeader(&b, "Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "signature.asc"}))
	writeMIMEHeader(&b, "Content-Transfer-Encoding", "7bit")
	b.WriteString("\r\n")
	signature := normalizeMIMECRLF(body.Metadata["pgp_signature"])
	b.WriteString(signature)
	if !strings.HasSuffix(signature, "\r\n") {
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return &plugins.MIMEBodyOverride{
		ContentType: mime.FormatMediaType("multipart/signed", map[string]string{
			"protocol": "application/pgp-signature",
			"micalg":   "pgp-sha256",
			"boundary": boundary,
		}),
		Body: b.String(),
	}
}

func writeMIMEHeader(b *strings.Builder, name, value string) {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	fmt.Fprintf(b, "%s: %s\r\n", name, strings.Join(strings.Fields(value), " "))
}

func normalizeMIMECRLF(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}

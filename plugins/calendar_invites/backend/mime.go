package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
)

// Decode calendar MIME parts independently of the attachment index: unnamed
// inline text/calendar parts are invitations too. Nothing is written to disk.
func calendarMIMEParts(raw []byte) [][]byte {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	var parts [][]byte
	remaining := int64(8 << 20)
	visited := 0
	var walk func(textproto.MIMEHeader, io.Reader, int)
	walk = func(header textproto.MIMEHeader, body io.Reader, depth int) {
		visited++
		if depth > 32 || visited > 256 || remaining <= 0 {
			return
		}
		mediaType, params, _ := mime.ParseMediaType(header.Get("Content-Type"))
		if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
			reader := multipart.NewReader(body, params["boundary"])
			for visited < 256 && remaining > 0 {
				part, err := reader.NextRawPart()
				if err != nil {
					break
				}
				walk(part.Header, part, depth+1)
				_ = part.Close()
			}
			return
		}
		_, disposition, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
		name := disposition["filename"]
		if name == "" {
			name = params["name"]
		}
		if !strings.EqualFold(mediaType, "text/calendar") && !strings.HasSuffix(strings.ToLower(name), ".ics") {
			return
		}
		switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, body)
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		}
		data, err := io.ReadAll(io.LimitReader(body, remaining+1))
		remaining -= int64(len(data))
		if err == nil && remaining >= 0 {
			parts = append(parts, data)
		}
	}
	walk(textproto.MIMEHeader(msg.Header), msg.Body, 0)
	return parts
}

// File overview: Helpers for Autocrypt header parsing, formatting, and
// converting OpenPGP public keys between ASCII armor and Autocrypt keydata.

package autocrypt

import (
	"encoding/base64"
	"net/mail"
	"strings"
)

const maxKeyDataBytes = 256 << 10

// Header is the validated subset of an Autocrypt or Autocrypt-Gossip header.
type Header struct {
	Addr          string
	PreferEncrypt string
	KeyData       string
	PublicKey     string
}

// ParseHeaderValues returns the valid Autocrypt headers from a MIME header map
// value. Invalid or oversized headers are ignored.
func ParseHeaderValues(values []string) []Header {
	out := make([]Header, 0, len(values))
	for _, value := range values {
		header, ok := ParseHeaderValue(value)
		if ok {
			out = append(out, header)
		}
	}
	return out
}

// ParseHeaderValue parses one unfolded Autocrypt-style header value.
func ParseHeaderValue(value string) (Header, bool) {
	value = strings.Join(strings.Fields(value), " ")
	if strings.TrimSpace(value) == "" {
		return Header{}, false
	}
	params := headerParams(value)
	addr := normalizeAddr(params["addr"])
	keyData, ok := normalizeKeyData(params["keydata"])
	if addr == "" || !ok {
		return Header{}, false
	}
	publicKey, ok := ArmoredPublicKeyFromKeyData(keyData)
	if !ok {
		return Header{}, false
	}
	return Header{
		Addr:          addr,
		PreferEncrypt: strings.ToLower(strings.TrimSpace(params["prefer-encrypt"])),
		KeyData:       keyData,
		PublicKey:     publicKey,
	}, true
}

func headerParams(value string) map[string]string {
	params := map[string]string{}
	for _, part := range strings.Split(value, ";") {
		name, rawValue, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		rawValue = strings.TrimSpace(rawValue)
		if strings.HasPrefix(rawValue, `"`) && strings.HasSuffix(rawValue, `"`) && len(rawValue) >= 2 {
			rawValue = strings.Trim(rawValue, `"`)
		}
		if name != "" {
			params[name] = rawValue
		}
	}
	return params
}

// HeaderValue returns an unfolded Autocrypt header value. The SMTP writer owns
// RFC5322 line folding because it knows the header field name.
func HeaderValue(addr, keyData string) string {
	addr = normalizeAddr(addr)
	keyData, ok := normalizeKeyData(keyData)
	if addr == "" || !ok {
		return ""
	}
	return "addr=" + addr + "; prefer-encrypt=mutual; keydata=" + keyData
}

// KeyDataFromArmoredPublicKey extracts normalized base64 Autocrypt keydata from
// an ASCII-armored OpenPGP public key block.
func KeyDataFromArmoredPublicKey(armored string) (string, bool) {
	var payload strings.Builder
	inBlock := false
	inPayload := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(armored, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.EqualFold(line, "-----BEGIN PGP PUBLIC KEY BLOCK-----") {
			inBlock = true
			continue
		}
		if strings.EqualFold(line, "-----END PGP PUBLIC KEY BLOCK-----") {
			break
		}
		if !inBlock {
			continue
		}
		if line == "" {
			inPayload = true
			continue
		}
		if !inPayload && strings.Contains(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "=") {
			continue
		}
		payload.WriteString(line)
	}
	return normalizeKeyData(payload.String())
}

// ArmoredPublicKeyFromKeyData converts Autocrypt keydata back into an
// ASCII-armored public key block. The CRC armor checksum is optional and omitted.
func ArmoredPublicKeyFromKeyData(keyData string) (string, bool) {
	keyData, ok := normalizeKeyData(keyData)
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n")
	for len(keyData) > 0 {
		n := 76
		if len(keyData) < n {
			n = len(keyData)
		}
		b.WriteString(keyData[:n])
		b.WriteString("\n")
		keyData = keyData[n:]
	}
	b.WriteString("-----END PGP PUBLIC KEY BLOCK-----")
	return b.String(), true
}

func normalizeAddr(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", ""))
	if value == "" {
		return ""
	}
	if addr, err := mail.ParseAddress(value); err == nil {
		value = addr.Address
	}
	value = strings.Trim(value, "<> \t")
	if !strings.Contains(value, "@") {
		return ""
	}
	return strings.ToLower(value)
}

func normalizeKeyData(value string) (string, bool) {
	value = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(value))
	if value == "" || base64.StdEncoding.DecodedLen(len(value)) > maxKeyDataBytes {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(value)
		if err != nil {
			return "", false
		}
	}
	if len(decoded) == 0 || len(decoded) > maxKeyDataBytes {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(decoded), true
}

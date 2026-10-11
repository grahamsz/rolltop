package maildiscovery

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// Decode one complete XML document. encoding/xml's Unmarshal ignores content
// after the first root, which would allow truncated or concatenated responses.
func decodeDocument(data []byte, dest any) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(dest); err != nil {
		return false
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		switch token := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return false
			}
		case xml.Comment:
		default:
			return false
		}
	}
}

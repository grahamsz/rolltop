package model

import (
	"math"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// FeatureSchema is persisted in model metadata and deliberately changes when
	// feature semantics change. Old weights must not be loaded with a new schema.
	FeatureSchema   = "rolltop-spam-features-v1"
	MaxBodyBytes    = 64 << 10
	MaxCharBytes    = 8 << 10
	maxCharFeatures = 4096
	maxURLFeatures  = 32
)

// Message is the bounded, in-memory view used by the classifier. Callers should
// pass decoded text; the model package never stores message content.
type Message struct {
	Subject         string   `json:"subject"`
	Body            string   `json:"body"`
	From            string   `json:"from"`
	To              []string `json:"to,omitempty"`
	MIMEType        string   `json:"mime_type,omitempty"`
	AttachmentTypes []string `json:"attachment_types,omitempty"`
	HTML            bool     `json:"html,omitempty"`
}

// Feature is exported for the offline trainer and for deterministic tests. Name
// is retained only in memory so runtime explanations can identify strong signals.
type Feature struct {
	Index uint32
	Name  string
	Value float64
}

// ExtractFeatures applies the exact feature schema consumed by checked-in model
// weights. dimension must be a non-zero power of two.
func ExtractFeatures(message Message, dimension uint32) ([]Feature, error) {
	return extractFeatures(message, dimension, true)
}

// ExtractCountFeatures applies the same normalization and feature families with
// unsigned hashing. It exists for count-based offline baselines such as
// multinomial naive Bayes; production logistic inference uses ExtractFeatures.
func ExtractCountFeatures(message Message, dimension uint32) ([]Feature, error) {
	return extractFeatures(message, dimension, false)
}

func extractFeatures(message Message, dimension uint32, signed bool) ([]Feature, error) {
	if dimension == 0 || dimension&(dimension-1) != 0 {
		return nil, ErrInvalidDimension
	}

	type accumulated struct {
		name  string
		value float64
	}
	features := make(map[uint32]accumulated, 8192)
	add := func(name string, value float64) {
		if name == "" || value == 0 {
			return
		}
		h := hashString(name)
		index := uint32(h) & (dimension - 1)
		if signed && h&(uint64(1)<<63) != 0 {
			value = -value
		}
		item := features[index]
		if item.name == "" {
			item.name = name
		}
		item.value += value
		features[index] = item
	}

	subjectTokens := tokenize(message.Subject)
	body := truncateUTF8(message.Body, MaxBodyBytes)
	bodyTokens := tokenize(body)
	addTokenFeatures(add, "subject", subjectTokens)
	addTokenFeatures(add, "body", bodyTokens)

	charText := normalizeChars(message.Subject + " " + truncateUTF8(body, MaxCharBytes))
	charCount := 0
	for size := 3; size <= 5 && charCount < maxCharFeatures; size++ {
		runes := []rune(charText)
		for i := 0; i+size <= len(runes) && charCount < maxCharFeatures; i++ {
			gram := string(runes[i : i+size])
			if strings.TrimSpace(gram) == "" {
				continue
			}
			add("char:"+gram, 1)
			charCount++
		}
	}

	if domain := addressDomain(message.From); domain != "" {
		add("from-domain:"+domain, 1)
	}
	if message.HTML || strings.Contains(strings.ToLower(message.MIMEType), "html") {
		add("structure:html", 1)
	}
	if mimeType := normalizeMIME(message.MIMEType); mimeType != "" {
		add("mime:"+mimeType, 1)
	}
	add("recipient-count:"+countBucket(len(message.To)), 1)
	for _, attachmentType := range message.AttachmentTypes {
		if value := normalizeMIME(attachmentType); value != "" {
			add("attachment:"+value, 1)
		}
	}
	for _, host := range URLHosts(message.Subject+" "+body, maxURLFeatures) {
		add("url-host:"+host, 1)
	}

	result := make([]Feature, 0, len(features))
	for index, item := range features {
		value := item.value
		sign := 1.0
		if value < 0 {
			sign = -1
			value = -value
		}
		if value > 1 {
			value = 1 + math.Log(value)
		}
		result = append(result, Feature{Index: index, Name: item.name, Value: sign * value})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Index < result[j].Index })
	return result, nil
}

func addTokenFeatures(add func(string, float64), field string, tokens []string) {
	for i, token := range tokens {
		add(field+":word:"+token, 1)
		if i > 0 {
			add(field+":bigram:"+tokens[i-1]+"_"+token, 1)
		}
	}
}

func tokenize(value string) []string {
	value = truncateUTF8(value, MaxBodyBytes)
	result := make([]string, 0, len(value)/6)
	var token []rune
	flush := func() {
		if len(token) > 1 {
			result = append(result, string(token))
		}
		token = token[:0]
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if len(token) < 48 {
				token = append(token, r)
			}
			continue
		}
		flush()
	}
	flush()
	return result
}

func normalizeChars(value string) string {
	var builder strings.Builder
	space := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			space = false
		} else if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func addressDomain(value string) string {
	if parsed, err := mail.ParseAddress(value); err == nil {
		value = parsed.Address
	}
	index := strings.LastIndexByte(value, '@')
	if index < 0 || index == len(value)-1 {
		return ""
	}
	return strings.Trim(strings.ToLower(value[index+1:]), " <>\t\r\n.")
}

func normalizeMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if index := strings.IndexByte(value, ';'); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func countBucket(count int) string {
	switch {
	case count <= 0:
		return "0"
	case count == 1:
		return "1"
	case count <= 3:
		return "2-3"
	case count <= 10:
		return "4-10"
	default:
		return "11+"
	}
}

// URLHosts returns a bounded, normalized list without performing network work.
func URLHosts(value string, limit int) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("<>\"'()[]{}", r)
	})
	result := make([]string, 0, limit)
	seen := make(map[string]struct{})
	for _, field := range fields {
		trimmed := strings.Trim(field, ".,;:!?\"'")
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		result = append(result, host)
		if len(result) == limit {
			break
		}
	}
	return result
}

func hashString(value string) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	hash := offset
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= prime
	}
	return hash
}

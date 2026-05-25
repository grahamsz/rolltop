// File overview: Language detection helpers.

package language_search

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	lingua "github.com/pemistahl/lingua-go"
)

const (
	maxSampleRunes = 6000
	minLetterRunes = 20
)

var detector = sync.OnceValue(func() lingua.LanguageDetector {
	return lingua.NewLanguageDetectorBuilder().
		FromAllSpokenLanguages().
		WithMinimumRelativeDistance(0.08).
		Build()
})

// DetectCode returns a lower-case ISO 639-1 language code for the strongest
// language signal in subject/body text. Empty means no reliable language.
func DetectCode(subject, body string) string {
	sample := sampleText(subject, body)
	if letterCount(sample) < minLetterRunes {
		return ""
	}
	lang, ok := detector().DetectLanguageOf(sample)
	if !ok || lang == lingua.Unknown {
		return ""
	}
	return NormalizeCode(lang.IsoCode639_1().String())
}

func NormalizeCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) != 2 {
		return ""
	}
	for _, r := range code {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	if lingua.GetLanguageFromIsoCode639_1(lingua.GetIsoCode639_1FromValue(code)) == lingua.Unknown {
		return ""
	}
	return code
}

func sampleText(subject, body string) string {
	text := strings.TrimSpace(strings.Join([]string{subject, body}, "\n\n"))
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= maxSampleRunes {
		return text
	}
	var b strings.Builder
	b.Grow(maxSampleRunes)
	runes := 0
	for _, r := range text {
		if runes >= maxSampleRunes {
			break
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

func letterCount(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

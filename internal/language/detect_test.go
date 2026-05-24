package language

import "testing"

func TestDetectCode(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		body    string
		want    string
	}{
		{
			name:    "japanese",
			subject: "お知らせ",
			body:    "これは日本語のメール本文です。週末の予定について確認してください。よろしくお願いします。",
			want:    "ja",
		},
		{
			name:    "french",
			subject: "Bonjour",
			body:    "Ceci est un message en français avec assez de contexte pour identifier correctement la langue.",
			want:    "fr",
		},
		{
			name:    "too short",
			subject: "Hi",
			body:    "Thanks",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectCode(tt.subject, tt.body); got != tt.want {
				t.Fatalf("DetectCode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeCode(t *testing.T) {
	if got := NormalizeCode("JA"); got != "ja" {
		t.Fatalf("NormalizeCode(JA) = %q", got)
	}
	if got := NormalizeCode("zz"); got != "" {
		t.Fatalf("NormalizeCode(zz) = %q", got)
	}
}

package remoteimages

import (
	"strings"
	"testing"
)

func TestExtractFindsImageSources(t *testing.T) {
	html := `<div style="background-image:url('https://cdn.example.test/bg.png')">
		<img src="//cdn.example.test/a.png" srcset="https://cdn.example.test/b.png 1x, https://cdn.example.test/c.png 2x">
		<img src="cid:inline">
	</div>`

	got := Extract(html)
	var urls []string
	for _, item := range got {
		urls = append(urls, item.URL+" "+item.Source)
	}
	joined := strings.Join(urls, "\n")
	for _, want := range []string{
		"https://cdn.example.test/a.png img-src",
		"https://cdn.example.test/b.png srcset",
		"https://cdn.example.test/c.png srcset",
		"https://cdn.example.test/bg.png css-url",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("extracted URLs:\n%s\nmissing %s", joined, want)
		}
	}
	if strings.Contains(joined, "cid:inline") {
		t.Fatalf("cid URL should not be extracted: %s", joined)
	}
}

func TestReplaceCachedRewritesKnownRemoteImages(t *testing.T) {
	original := "https://cdn.example.test/a.png"
	html := `<img src="https://cdn.example.test/a.png"><div style="background-image:url(https://cdn.example.test/bg.png)"></div>`
	cache := map[string]string{
		Hash(original):                          "/remote-images/" + Hash(original),
		Hash("https://cdn.example.test/bg.png"): "/remote-images/bg",
	}

	got := ReplaceCached(html, cache)

	if !strings.Contains(got, `/remote-images/`+Hash(original)) {
		t.Fatalf("src was not rewritten: %s", got)
	}
	if !strings.Contains(got, `url(/remote-images/bg)`) {
		t.Fatalf("css URL was not rewritten: %s", got)
	}
}

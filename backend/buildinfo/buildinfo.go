// File overview: Build-time release metadata surfaced in API payloads,
// response headers, and the frontend chrome. CI sets these values with Go
// ldflags; local development falls back to a dev label.

package buildinfo

import "strings"

const PublicSiteURL = "https://rolltop.app"

var (
	Version   = "latest"
	BuildDate = ""
	Commit    = ""
)

type Info struct {
	Version       string
	BuildDate     string
	Commit        string
	Label         string
	PublicSiteURL string
}

func Current() Info {
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "latest"
	}
	buildDate := strings.TrimSpace(BuildDate)
	label := version
	if strings.EqualFold(version, "latest") {
		label = buildDate
	}
	if strings.TrimSpace(label) == "" {
		label = "dev"
	}
	return Info{
		Version:       version,
		BuildDate:     buildDate,
		Commit:        strings.TrimSpace(Commit),
		Label:         label,
		PublicSiteURL: PublicSiteURL,
	}
}

// File overview: Generic hook contracts for statically compiled plugin modules.

package plugins

import (
	"context"
	"database/sql"
	"io"
	"time"
)

// LanguageSearchHook keeps language detection and query normalization behind
// the plugin registry while allowing search/index code to stay plugin-neutral.
type LanguageSearchHook interface {
	DetectLanguage(subject, body string) string
	NormalizeLanguageCode(code string) string
}

// AttachmentPreviewInput is the core-owned attachment shape used by preview
// plugins for classification and route metadata.
type AttachmentPreviewInput struct {
	ID          int64
	Filename    string
	ContentType string
}

// AttachmentPreviewResult describes a browser-preview action exposed by a
// compiled attachment preview plugin.
type AttachmentPreviewResult struct {
	Available bool
	Kind      string
	URL       string
	Status    string
	PluginID  string
}

// AttachmentPreviewHook provides preview metadata and content-type validation
// for authenticated attachment preview routes.
type AttachmentPreviewHook interface {
	PreviewForAttachment(AttachmentPreviewInput) (AttachmentPreviewResult, bool)
	PreviewKind(AttachmentPreviewInput) string
	MaxPreviewBytes() int64
	CleanPreviewContentType(string) string
	SupportedPreviewImageType(string) bool
	PreviewImageTypeFromName(string) string
	ReadPreviewLimited(io.Reader, int64) ([]byte, error)
}

type BIMIResult struct {
	Domain    string
	LogoURL   string
	SVG       string
	Status    string
	Error     string
	FetchedAt time.Time
	ExpiresAt time.Time
}

type BIMIIcon struct {
	ID        int64
	UserID    int64
	Domain    string
	LogoURL   string
	SVG       string
	Status    string
	Error     string
	FetchedAt time.Time
	ExpiresAt time.Time
	UpdatedAt time.Time
}

type BIMIIconMeta struct {
	ID        int64
	UserID    int64
	Domain    string
	LogoURL   string
	Status    string
	Error     string
	HasSVG    bool
	FetchedAt time.Time
	ExpiresAt time.Time
	UpdatedAt time.Time
}

type BIMIHook interface {
	NormalizeDomain(string) string
	AssetURL(string) string
	GetIcon(context.Context, *sql.DB, int64, string) (BIMIIcon, error)
	UpsertIcon(context.Context, *sql.DB, BIMIIcon) error
	Fetch(context.Context, string) (BIMIResult, error)
}

type BIMIIconMetaHook interface {
	GetIconMeta(context.Context, *sql.DB, int64, string) (BIMIIconMeta, error)
}

type GravatarImage struct {
	ID          int64
	UserID      int64
	EmailHash   string
	ContentType string
	Image       []byte
	Status      string
	Error       string
	FetchedAt   time.Time
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

type GravatarImageMeta struct {
	ID          int64
	UserID      int64
	EmailHash   string
	ContentType string
	Status      string
	Error       string
	HasImage    bool
	FetchedAt   time.Time
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

type GravatarHook interface {
	NormalizeHash(string) string
	AssetURL(string) string
	Hash(string) string
	GetImage(context.Context, *sql.DB, int64, string) (GravatarImage, error)
	UpsertImage(context.Context, *sql.DB, GravatarImage) error
	FetchURL(string) string
	ErrorTTL(time.Time) time.Time
	MissingTTL(time.Time) time.Time
	PositiveTTL(time.Time) time.Time
	MaxImageBytes() int64
	ReadLimited(io.Reader, int64) ([]byte, error)
}

type GravatarImageMetaHook interface {
	GetImageMeta(context.Context, *sql.DB, int64, string) (GravatarImageMeta, error)
}

type RemoteImageRule struct {
	Pattern string
	Enabled bool
}

type RemoteImageFetchRequest struct {
	UserID    int64
	MessageID string
	Mailbox   string
	Sender    string
	URL       string
	Source    string
}

type RemoteImageFetchDecision struct {
	Allow  bool
	Reason string
}

type RemoteImageBlocklistHook interface {
	SeedRemoteImageRules(context.Context, *sql.DB) error
	ListRemoteImageRules(context.Context, *sql.DB) ([]RemoteImageRule, error)
	ListRemoteImagePatterns(context.Context, *sql.DB) ([]string, error)
	ReplaceRemoteImageRules(context.Context, *sql.DB, []string) error
	AllowRemoteImageFetch(context.Context, *sql.DB, RemoteImageFetchRequest) (RemoteImageFetchDecision, error)
}

type OneClickUnsubscribeSend struct {
	SentAt time.Time
}

type OneClickUnsubscribeHook interface {
	LatestOneClickSend(context.Context, *sql.DB, int64, int64, string, time.Time) (OneClickUnsubscribeSend, error)
	RecordOneClickSend(context.Context, *sql.DB, int64, int64, string, string, time.Time) error
}

type TrustedImageSourcesHook interface {
	TrustImageSender(context.Context, *sql.DB, int64, string) error
	IsImageSenderTrusted(context.Context, *sql.DB, int64, string) (bool, error)
}

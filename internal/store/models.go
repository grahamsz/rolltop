package store

import "time"

type User struct {
	ID           int64
	Email        string
	Name         string
	PasswordHash string
	IsAdmin      bool
	DateLocale   string
	DateFormat   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Session struct {
	ID         int64
	UserID     int64
	TokenHash  string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
}

type MailAccount struct {
	ID                    int64
	UserID                int64
	Email                 string
	Host                  string
	Port                  int
	Username              string
	EncryptedPassword     string
	UseTLS                bool
	SMTPHost              string
	SMTPPort              int
	SMTPUsername          string
	EncryptedSMTPPassword string
	SMTPUseTLS            bool
	Mailbox               string
	SyncIntervalMinutes   int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Mailbox struct {
	ID                 int64
	UserID             int64
	AccountID          int64
	Name               string
	SyncMode           string
	Role               string
	Icon               string
	ShowInSidebar      bool
	ShowInAllMail      bool
	IncludeInSearch    bool
	UIDValidity        int64
	LastUID            uint32
	RemoteMessageCount int
	RemoteUnreadCount  int
	RemoteUIDNext      uint32
	StatusCheckedAt    time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type MailboxSummary struct {
	Mailbox
	AccountEmail string
	MessageCount int
	UnreadCount  int
	SyncPercent  int
}

type MailboxSettings struct {
	SyncMode        string
	Role            string
	Icon            string
	ShowInSidebar   bool
	ShowInAllMail   bool
	IncludeInSearch bool
}

type MessageRecord struct {
	ID                  int64
	UserID              int64
	AccountID           int64
	MailboxID           int64
	BlobID              int64
	MessageIDHeader     string
	InReplyTo           string
	ReferencesHeader    string
	ThreadKey           string
	Subject             string
	LanguageCode        string
	FromAddr            string
	ToAddr              string
	CCAddr              string
	Date                time.Time
	InternalDate        time.Time
	UID                 uint32
	Size                int64
	BlobPath            string
	BodyText            string
	BodyHTML            string
	IsRead              bool
	ReadSyncPending     bool
	IsStarred           bool
	StarSyncPending     bool
	HasAttachments      bool
	AttachmentIndexedAt time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type SenderReadStat struct {
	Sender     string
	ReadCount  int
	TotalCount int
	Boost      float64
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

type RemoteImageBlockRule struct {
	ID        int64
	Pattern   string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type OneClickUnsubscribeSend struct {
	ID             int64
	UserID         int64
	MessageID      int64
	Sender         string
	UnsubscribeURL string
	SentAt         time.Time
	CreatedAt      time.Time
}

type Attachment struct {
	ID          int64
	UserID      int64
	MessageID   int64
	BlobID      int64
	Filename    string
	ContentType string
	ContentID   string
	IsInline    bool
	Size        int64
	BlobPath    string
	CreatedAt   time.Time
}

type BlobRecord struct {
	ID        int64
	UserID    int64
	Kind      string
	Path      string
	SHA256    string
	Size      int64
	CreatedAt time.Time
}

type SyncRun struct {
	ID              int64
	UserID          int64
	AccountID       int64
	Status          string
	StartedAt       time.Time
	FinishedAt      time.Time
	UpdatedAt       time.Time
	MessagesSeen    int
	MessagesStored  int
	MessagesSkipped int
	NewMessages     int
	MessagesTotal   int
	MailboxesDone   int
	MailboxesTotal  int
	CurrentMailbox  string
	CurrentUID      uint32
	Error           string
}

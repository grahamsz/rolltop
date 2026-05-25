// File overview: Shared store model types. These structs describe users, accounts, mailboxes, messages, attachments, contacts, identities, sync runs, and blob records exchanged across backend packages.

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
	Theme        string
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
	Label                 string
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

type SMTPAccount struct {
	ID                int64
	UserID            int64
	Label             string
	Host              string
	Port              int
	Username          string
	EncryptedPassword string
	UseTLS            bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type MailIdentity struct {
	ID             int64
	UserID         int64
	ContactID      int64
	ContactEmailID int64
	SMTPAccountID  int64
	Email          string
	DisplayName    string
	Signature      string
	IsPrimary      bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	AccountEmail       string
	MessageCount       int
	UnreadCount        int
	LocalMessageCount  int
	LocalSyncPercent   int
	SyncPercent        int
	SearchIndexedCount *int
	SearchIndexTotal   *int
	SearchIndexPercent *int
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

type Contact struct {
	ID             int64
	UserID         int64
	NamePrefix     string
	GivenName      string
	AdditionalName string
	FamilyName     string
	NameSuffix     string
	DisplayName    string
	Nickname       string
	Organization   string
	Department     string
	JobTitle       string
	Birthday       string
	Notes          string
	Categories     string
	IsMe           bool
	IsPrimary      bool
	Emails         []ContactEmail
	Phones         []ContactPhone
	Addresses      []ContactAddress
	URLs           []ContactURL
	Icon           *ContactIcon
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ContactEmail struct {
	ID              int64
	UserID          int64
	ContactID       int64
	Label           string
	Email           string
	NormalizedEmail string
	IsPrimary       bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ContactPhone struct {
	ID        int64
	UserID    int64
	ContactID int64
	Label     string
	Number    string
	IsPrimary bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ContactAddress struct {
	ID         int64
	UserID     int64
	ContactID  int64
	Label      string
	Street     string
	Locality   string
	Region     string
	PostalCode string
	Country    string
	IsPrimary  bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ContactURL struct {
	ID        int64
	UserID    int64
	ContactID int64
	Label     string
	URL       string
	IsPrimary bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ContactIcon struct {
	ID          int64
	UserID      int64
	ContactID   int64
	BlobID      int64
	ContentType string
	Filename    string
	Size        int64
	BlobPath    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ContactAutocomplete struct {
	ContactID int64
	Name      string
	Email     string
	Label     string
	IconURL   string
}

type SyncRun struct {
	ID               int64
	UserID           int64
	AccountID        int64
	Status           string
	StartedAt        time.Time
	FinishedAt       time.Time
	UpdatedAt        time.Time
	MessagesSeen     int
	MessagesStored   int
	MessagesSkipped  int
	NewMessages      int
	LatestNewFrom    string
	LatestNewSubject string
	MessagesTotal    int
	MailboxesDone    int
	MailboxesTotal   int
	CurrentMailbox   string
	CurrentUID       uint32
	Error            string
}

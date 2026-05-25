package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr         string
	DataDir      string
	DatabasePath string
	IndexPath    string

	MasterKey []byte

	SessionTTL        time.Duration
	CookieSecure      bool
	SyncInterval      time.Duration
	InboxPollInterval time.Duration
	BlobRetention     time.Duration
	WebhookToken      string
}

func Load() (Config, error) {
	dataDir := env("MAILMIRROR_DATA_DIR", "/data")
	dbPath := env("MAILMIRROR_DB_PATH", filepath.Join(dataDir, "mailmirror.db"))
	indexPath := env("MAILMIRROR_INDEX_PATH", filepath.Join(dataDir, "bleve"))

	key, err := ParseMasterKey(os.Getenv("MAILMIRROR_MASTER_KEY"))
	if err != nil {
		return Config{}, err
	}

	sessionTTL, err := parseDuration("MAILMIRROR_SESSION_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	syncInterval, err := parseDuration("MAILMIRROR_SYNC_INTERVAL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	inboxPollInterval, err := parseDuration("MAILMIRROR_INBOX_POLL_INTERVAL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	blobRetention, err := parseDuration("MAILMIRROR_BLOB_RETENTION", 14*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cookieSecure, err := parseBool("MAILMIRROR_COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:              env("MAILMIRROR_ADDR", ":8080"),
		DataDir:           dataDir,
		DatabasePath:      dbPath,
		IndexPath:         indexPath,
		MasterKey:         key,
		SessionTTL:        sessionTTL,
		CookieSecure:      cookieSecure,
		SyncInterval:      syncInterval,
		InboxPollInterval: inboxPollInterval,
		BlobRetention:     blobRetention,
		WebhookToken:      os.Getenv("MAILMIRROR_WEBHOOK_TOKEN"),
	}, nil
}

func ParseMasterKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("MAILMIRROR_MASTER_KEY is required")
	}

	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		if b, err := decode(value); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len([]byte(value)) == 32 {
		return []byte(value), nil
	}
	return nil, errors.New("MAILMIRROR_MASTER_KEY must decode to exactly 32 bytes")
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func parseBool(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

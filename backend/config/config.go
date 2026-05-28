// File overview: Environment-driven application configuration.

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

// Config is the validated runtime configuration assembled from environment variables.
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

// Load reads environment configuration, applies defaults, and validates values needed before services start.
func Load() (Config, error) {
	dataDir := env("ROLLTOP_DATA_DIR", legacyEnvPrefix+"DATA_DIR", "/data")
	dbPath := databasePath(dataDir)
	indexPath := env("ROLLTOP_INDEX_PATH", legacyEnvPrefix+"INDEX_PATH", filepath.Join(dataDir, "bleve"))

	key, err := ParseMasterKey(env("ROLLTOP_MASTER_KEY", legacyEnvPrefix+"MASTER_KEY", ""))
	if err != nil {
		return Config{}, err
	}

	sessionTTL, err := parseDuration("ROLLTOP_SESSION_TTL", legacyEnvPrefix+"SESSION_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	syncInterval, err := parseDuration("ROLLTOP_SYNC_INTERVAL", legacyEnvPrefix+"SYNC_INTERVAL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	inboxPollInterval, err := parseDuration("ROLLTOP_INBOX_POLL_INTERVAL", legacyEnvPrefix+"INBOX_POLL_INTERVAL", time.Minute)
	if err != nil {
		return Config{}, err
	}
	blobRetention, err := parseDuration("ROLLTOP_BLOB_RETENTION", legacyEnvPrefix+"BLOB_RETENTION", 14*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cookieSecure, err := parseBool("ROLLTOP_COOKIE_SECURE", legacyEnvPrefix+"COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:              env("ROLLTOP_ADDR", legacyEnvPrefix+"ADDR", ":8080"),
		DataDir:           dataDir,
		DatabasePath:      dbPath,
		IndexPath:         indexPath,
		MasterKey:         key,
		SessionTTL:        sessionTTL,
		CookieSecure:      cookieSecure,
		SyncInterval:      syncInterval,
		InboxPollInterval: inboxPollInterval,
		BlobRetention:     blobRetention,
		WebhookToken:      env("ROLLTOP_WEBHOOK_TOKEN", legacyEnvPrefix+"WEBHOOK_TOKEN", ""),
	}, nil
}

// ParseMasterKey decodes the encryption key used for IMAP/SMTP secrets and enforces the required key length.
func ParseMasterKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("ROLLTOP_MASTER_KEY is required")
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
	return nil, errors.New("ROLLTOP_MASTER_KEY must decode to exactly 32 bytes")
}

const (
	databaseFilename       = "rolltop.db"
	legacyDatabaseFilename = "mailmirror.db"
	legacyEnvPrefix        = "MAILMIRROR_"
)

func databasePath(dataDir string) string {
	if v := strings.TrimSpace(os.Getenv("ROLLTOP_DB_PATH")); v != "" {
		if filepath.Base(v) == legacyDatabaseFilename {
			return filepath.Join(filepath.Dir(v), databaseFilename)
		}
		return v
	}
	if v := strings.TrimSpace(os.Getenv(legacyEnvPrefix + "DB_PATH")); v != "" {
		if filepath.Base(v) == legacyDatabaseFilename {
			return filepath.Join(filepath.Dir(v), databaseFilename)
		}
		return v
	}
	return filepath.Join(dataDir, databaseFilename)
}

func env(key, legacyKey, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if legacyKey != "" {
		if v := strings.TrimSpace(os.Getenv(legacyKey)); v != "" {
			return v
		}
	}
	return fallback
}

func parseDuration(key, legacyKey string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	source := key
	if v == "" && legacyKey != "" {
		v = strings.TrimSpace(os.Getenv(legacyKey))
		source = legacyKey
	}
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", source, err)
	}
	return d, nil
}

func parseBool(key, legacyKey string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	source := key
	if v == "" && legacyKey != "" {
		v = strings.TrimSpace(os.Getenv(legacyKey))
		source = legacyKey
	}
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}
	return b, nil
}

// File overview: Store construction and tenant database routing. OpenServer
// opens only the system database; UserStore opens data/users/<id>/mailmirror.db
// on demand and mirrors the system user row into it. Store methods that touch
// user-owned mail/contact/blob/search hydration metadata should call dataDB or
// mustDataDB so they automatically run against the per-user SQLite handle in
// split mode while tests can still use one combined database via Open.

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var ErrNotFound = sql.ErrNoRows

const DefaultMessageBodyPreviewBytes = 4096

type Store struct {
	db         *sql.DB
	dataDir    string
	split      bool
	mu         sync.Mutex
	userStores map[int64]*Store
}

// Open creates a combined store in one SQLite file. It is mostly used by tests
// and small helpers that do not need the production system/user split.
func Open(path string) (*Store, error) {
	return open(path, "", false, schemaCombined, nil)
}

// OpenServer opens the production system store without progress reporting.
// cmd/mailmirror usually calls OpenServerWithProgress instead.
func OpenServer(path string, dataDir string) (*Store, error) {
	return OpenServerWithProgress(path, dataDir, nil)
}

// OpenServerWithProgress opens the installation-level database only. Per-user
// databases are opened lazily through UserStore so tenant-owned data remains in
// data/users/<id>/mailmirror.db.
func OpenServerWithProgress(path string, dataDir string, progress MigrationReporter) (*Store, error) {
	return open(path, dataDir, true, schemaSystem, progress)
}

func open(path string, dataDir string, split bool, schema schemaKind, progress MigrationReporter) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dataDir: dataDir, split: split}
	if split {
		s.userStores = make(map[int64]*Store)
	}
	if err := s.migrate(context.Background(), schema, progress); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close shuts down the root store and any cached per-user stores opened through
// UserStore. The first close error is returned after all handles are attempted.
func (s *Store) Close() error {
	s.mu.Lock()
	stores := make([]*Store, 0, len(s.userStores))
	for _, us := range s.userStores {
		stores = append(stores, us)
	}
	s.mu.Unlock()
	var first error
	for _, us := range stores {
		if err := us.Close(); err != nil && first == nil {
			first = err
		}
	}
	if err := s.db.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// UserDataDir returns the filesystem directory that owns one user's SQLite DB,
// blobs, and search index. An empty dataDir means the store is combined.
func (s *Store) UserDataDir(userID int64) string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, "users", fmt.Sprintf("%d", userID))
}

// UserStore returns the per-user database handle for user-owned data. In split
// mode this opens and migrates the user database lazily; in combined mode it
// returns the receiver.
func (s *Store) UserStore(ctx context.Context, userID int64) (*Store, error) {
	return s.userStore(ctx, userID, nil)
}

// PrepareUserStores is called during process startup so existing users have
// their schemas migrated before background sync or HTTP requests touch them.
func (s *Store) PrepareUserStores(ctx context.Context, progress MigrationReporter) error {
	if !s.split {
		return nil
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		return err
	}
	for i, user := range users {
		reportMigration(progress, MigrationProgress{Scope: "user", Migration: "open user database", Step: fmt.Sprintf("user %d", user.ID), Done: i, Total: len(users)})
		if _, err := s.userStore(ctx, user.ID, progress); err != nil {
			return err
		}
		reportMigration(progress, MigrationProgress{Scope: "user", Migration: "open user database", Step: fmt.Sprintf("user %d", user.ID), Done: i + 1, Total: len(users)})
	}
	return nil
}

func (s *Store) userStore(ctx context.Context, userID int64, progress MigrationReporter) (*Store, error) {
	if !s.split || userID == 0 {
		return s, nil
	}
	s.mu.Lock()
	if us := s.userStores[userID]; us != nil {
		s.mu.Unlock()
		return us, nil
	}
	s.mu.Unlock()
	user, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	userDir := s.UserDataDir(userID)
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return nil, err
	}
	userDBPath := filepath.Join(userDir, "mailmirror.db")
	us, err := open(userDBPath, "", false, schemaUser, progress)
	if err != nil {
		return nil, err
	}
	if err := us.mirrorUser(ctx, user); err != nil {
		_ = us.Close()
		return nil, err
	}
	s.mu.Lock()
	if existing := s.userStores[userID]; existing != nil {
		s.mu.Unlock()
		_ = us.Close()
		return existing, nil
	}
	s.userStores[userID] = us
	s.mu.Unlock()
	return us, nil
}

func (s *Store) UserDB(ctx context.Context, userID int64) (*sql.DB, error) {
	us, err := s.UserStore(ctx, userID)
	if err != nil {
		return nil, err
	}
	return us.db, nil
}

// dataDB is the central tenant-routing helper. Any method that reads or writes
// user-owned mail/contact/blob metadata should reach SQLite through this path.
func (s *Store) dataDB(ctx context.Context, userID int64) (*sql.DB, error) {
	if !s.split || userID == 0 {
		return s.db, nil
	}
	return s.UserDB(ctx, userID)
}

func (s *Store) mustDataDB(ctx context.Context, userID int64) *sql.DB {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		panic(err)
	}
	return db
}

func (s *Store) mirrorUser(ctx context.Context, user User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users
			(id, email, name, password_hash, is_admin, date_locale, date_format, theme, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			email = excluded.email,
			name = excluded.name,
			password_hash = excluded.password_hash,
			is_admin = excluded.is_admin,
			date_locale = excluded.date_locale,
			date_format = excluded.date_format,
			theme = excluded.theme,
			updated_at = excluded.updated_at`,
		user.ID, user.Email, user.Name, user.PasswordHash, boolInt(user.IsAdmin), user.DateLocale, user.DateFormat, user.Theme, user.CreatedAt.UTC().Unix(), user.UpdatedAt.UTC().Unix())
	return err
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

func nowUnix() int64 {
	return time.Now().UTC().Unix()
}

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

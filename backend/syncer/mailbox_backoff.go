// File overview: Per-folder sync failure backoff.
//
// When a folder's fetch keeps failing (for example a provider throttling
// sequential IMAP connections), retrying it on every 15-minute cycle burns
// connections and can starve the remaining folders. The backoff below skips a
// consecutively failing folder for a growing delay while the other folders
// keep syncing. A successful folder sync clears its backoff immediately.
//
// The state is in-memory only: a restart simply retries every folder once.
// The generation-recovery worker bypasses the skip because it is the
// designated retry path for folders with a pending generation rebuild.

package syncer

import (
	"strings"
	"time"
)

// mailboxBackoffBaseDelay is the skip after the first consecutive per-folder
// failure. It doubles per consecutive failure up to mailboxBackoffMaxDelay.
// Kept as vars (not consts) so focused tests can shrink them.
var (
	mailboxBackoffBaseDelay = 15 * time.Minute
	mailboxBackoffMaxDelay  = 4 * time.Hour
)

// mailboxBackoffKey scopes backoff state to one tenant's folder.
type mailboxBackoffKey struct {
	userID    int64
	accountID int64
	mailbox   string
}

// mailboxBackoffState counts consecutive failures and holds the next attempt.
type mailboxBackoffState struct {
	failures    int
	nextAttempt time.Time
}

// mailboxBackoffDelay returns the skip for the given consecutive failure
// count: base, 2x base, 4x base, ... capped at the maximum.
func mailboxBackoffDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := mailboxBackoffBaseDelay
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= mailboxBackoffMaxDelay {
			return mailboxBackoffMaxDelay
		}
	}
	return delay
}

func newMailboxBackoffKey(userID, accountID int64, mailbox string) mailboxBackoffKey {
	return mailboxBackoffKey{
		userID:    userID,
		accountID: accountID,
		mailbox:   strings.ToLower(strings.TrimSpace(mailbox)),
	}
}

// mailboxSyncDelayed reports whether the folder is currently backed off after
// consecutive failures.
func (s *Service) mailboxSyncDelayed(userID, accountID int64, mailbox string) bool {
	key := newMailboxBackoffKey(userID, accountID, mailbox)
	s.mailboxBackoffMu.Lock()
	defer s.mailboxBackoffMu.Unlock()
	state, ok := s.mailboxBackoff[key]
	if !ok || state.failures == 0 {
		return false
	}
	return time.Now().Before(state.nextAttempt)
}

// recordMailboxSyncSuccess clears any backoff for the folder.
func (s *Service) recordMailboxSyncSuccess(userID, accountID int64, mailbox string) {
	key := newMailboxBackoffKey(userID, accountID, mailbox)
	s.mailboxBackoffMu.Lock()
	defer s.mailboxBackoffMu.Unlock()
	delete(s.mailboxBackoff, key)
}

// recordMailboxSyncFailure increments the folder's consecutive failure count
// and pushes its next attempt out by the backoff delay.
func (s *Service) recordMailboxSyncFailure(userID, accountID int64, mailbox string) {
	key := newMailboxBackoffKey(userID, accountID, mailbox)
	s.mailboxBackoffMu.Lock()
	defer s.mailboxBackoffMu.Unlock()
	if s.mailboxBackoff == nil {
		s.mailboxBackoff = make(map[mailboxBackoffKey]mailboxBackoffState)
	}
	state := s.mailboxBackoff[key]
	state.failures++
	state.nextAttempt = time.Now().Add(mailboxBackoffDelay(state.failures))
	s.mailboxBackoff[key] = state
}

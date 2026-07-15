package web

import "testing"

func TestNotifySyncProgressEmitsEventWithoutInvalidatingMailList(t *testing.T) {
	const userID int64 = 91
	server := &Server{events: newEventHub(), mailListCache: newMailListCache()}
	key := mailListCacheKey{UserID: userID, Page: 1}
	server.rememberMailListPage(key, `"mail-list"`, []byte(`{"page":1}`), server.mailListGeneration(userID))
	beforeGeneration := server.mailListGeneration(userID)
	changed, unsubscribe := server.events.Subscribe(userID)
	defer unsubscribe()

	server.notifySyncProgress(userID)

	select {
	case <-changed:
	default:
		t.Fatal("sync progress did not wake the user's SSE stream")
	}
	if got := server.mailListGeneration(userID); got != beforeGeneration {
		t.Fatalf("mail-list generation = %d, want unchanged %d", got, beforeGeneration)
	}
	if _, ok := server.mailListCache.page(key); !ok {
		t.Fatal("sync progress invalidated the cached mail list")
	}
}

package web

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestNewMailWebPushUsesPerSubscriptionEventDeltaAndTenant(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "native-push-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "native-push-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerSub := saveNotifierTestSubscription(t, ctx, db, owner.ID, "https://push.example.test/owner")
	otherSub := saveNotifierTestSubscription(t, ctx, db, other.ID, "https://push.example.test/other")
	ownerFirst := createNotificationTestMessage(t, ctx, db, owner, 701, "Alice <alice@example.test>", "First")
	ownerSecond := createNotificationTestMessage(t, ctx, db, owner, 702, "Bob <bob@example.test>", "Second")
	otherMessage := createNotificationTestMessage(t, ctx, db, other, 801, "Secret <secret@example.test>", "Other tenant")
	for _, message := range []store.MessageRecord{ownerFirst, ownerSecond, otherMessage} {
		if _, _, err := db.RecordNewMailEvent(ctx, message.UserID, message); err != nil {
			t.Fatal(err)
		}
	}

	var delivered []webPushNotification
	server := &Server{
		store:     db,
		masterKey: []byte("12345678901234567890123456789012"),
		webPushSend: func(_ context.Context, _ *http.Client, _ []byte, sub store.WebPushSubscription, payload []byte, _ string) (webPushSendResult, error) {
			if sub.Endpoint != ownerSub.Endpoint {
				t.Fatalf("delivered owner notification to %q", sub.Endpoint)
			}
			var note webPushNotification
			if err := json.Unmarshal(payload, &note); err != nil {
				t.Fatal(err)
			}
			delivered = append(delivered, note)
			return webPushSendResult{StatusCode: http.StatusCreated}, nil
		},
	}
	server.notifyNewMailWebPush(ctx, owner.ID)
	if len(delivered) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(delivered))
	}
	note := delivered[0]
	if note.Title != "rolltop - Bob" || note.Body != "2 new messages synced. Latest: Second" || note.URL != "/mail" || note.MessageID != 0 {
		t.Fatalf("multiple-message notification = %+v", note)
	}
	ownerSubs, err := db.ListWebPushSubscriptions(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerSubs) != 1 || ownerSubs[0].LastNewMailEventID <= ownerSub.LastNewMailEventID {
		t.Fatalf("owner cursor did not advance: %+v", ownerSubs)
	}
	otherSubs, err := db.ListWebPushSubscriptions(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherSubs) != 1 || otherSubs[0].ID != otherSub.ID || otherSubs[0].LastNewMailEventID != otherSub.LastNewMailEventID {
		t.Fatalf("owner delivery changed other tenant: %+v", otherSubs)
	}

	third := createNotificationTestMessage(t, ctx, db, owner, 703, "Carol <carol@example.test>", "Third")
	thirdEvent, _, err := db.RecordNewMailEvent(ctx, owner.ID, third)
	if err != nil {
		t.Fatal(err)
	}
	server.webPushSend = func(_ context.Context, _ *http.Client, _ []byte, _ store.WebPushSubscription, _ []byte, _ string) (webPushSendResult, error) {
		return webPushSendResult{StatusCode: http.StatusServiceUnavailable}, errors.New("temporary push failure")
	}
	server.notifyNewMailWebPush(ctx, owner.ID)
	ownerSubs, err = db.ListWebPushSubscriptions(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerSubs[0].LastNewMailEventID == thirdEvent.ID {
		t.Fatal("failed delivery advanced the subscription cursor")
	}
	server.webPushSend = func(_ context.Context, _ *http.Client, _ []byte, _ store.WebPushSubscription, payload []byte, _ string) (webPushSendResult, error) {
		var single webPushNotification
		if err := json.Unmarshal(payload, &single); err != nil {
			t.Fatal(err)
		}
		if single.MessageID != third.ID || single.URL != webPushMessageURL(third.ID) || single.Body != "Third" {
			t.Fatalf("single-message notification = %+v", single)
		}
		return webPushSendResult{StatusCode: http.StatusOK}, nil
	}
	server.notifyNewMailWebPush(ctx, owner.ID)
	ownerSubs, err = db.ListWebPushSubscriptions(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerSubs[0].LastNewMailEventID != thirdEvent.ID {
		t.Fatalf("successful retry cursor = %d, want %d", ownerSubs[0].LastNewMailEventID, thirdEvent.ID)
	}
}

func TestNewMailWebPushRetriesAfterInFlightKeyRotation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rotated-push@example.test", "Rotated", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	original := saveNotifierTestSubscription(t, ctx, db, user.ID, "https://push.example.test/rotated")
	message := createNotificationTestMessage(t, ctx, db, user, 751, "Sender", "Rotated keys")
	event, _, err := db.RecordNewMailEvent(ctx, user.ID, message)
	if err != nil {
		t.Fatal(err)
	}
	x, y := elliptic.P256().ScalarBaseMult([]byte{99})
	rotatedP256DH := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), x, y))
	rotatedAuth := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{99}, 16))
	server := &Server{
		store: db,
		webPushSend: func(_ context.Context, _ *http.Client, _ []byte, delivered store.WebPushSubscription, _ []byte, _ string) (webPushSendResult, error) {
			if delivered.P256DH != original.P256DH {
				t.Fatalf("first delivery used unexpected keys")
			}
			if _, err := db.SaveWebPushSubscription(ctx, user.ID, store.WebPushSubscription{
				Endpoint: original.Endpoint,
				P256DH:   rotatedP256DH,
				Auth:     rotatedAuth,
			}); err != nil {
				t.Fatal(err)
			}
			return webPushSendResult{StatusCode: http.StatusCreated}, nil
		},
	}
	if retry := server.notifyNewMailWebPush(ctx, user.ID); !retry {
		t.Fatal("in-flight key rotation did not leave the event eligible for retry")
	}
	subs, err := db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].P256DH != rotatedP256DH || subs[0].LastNewMailEventID == event.ID {
		t.Fatalf("rotated subscription advanced with old-key delivery: %+v", subs)
	}

	server.webPushSend = func(_ context.Context, _ *http.Client, _ []byte, delivered store.WebPushSubscription, _ []byte, _ string) (webPushSendResult, error) {
		if delivered.P256DH != rotatedP256DH || delivered.Auth != rotatedAuth {
			t.Fatalf("retry did not use rotated key material")
		}
		return webPushSendResult{StatusCode: http.StatusCreated}, nil
	}
	if retry := server.notifyNewMailWebPush(ctx, user.ID); retry {
		t.Fatal("successful delivery with current keys requested another retry")
	}
	subs, err = db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].LastNewMailEventID != event.ID {
		t.Fatalf("current-key retry cursor = %+v, want event %d", subs, event.ID)
	}
}

func TestNewMailWebPushStaleResponseDoesNotDeleteRotatedSubscription(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "stale-rotation@example.test", "Stale Rotation", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	original := saveNotifierTestSubscription(t, ctx, db, user.ID, "https://push.example.test/stale-rotation")
	message := createNotificationTestMessage(t, ctx, db, user, 752, "Sender", "Replacement endpoint")
	event, _, err := db.RecordNewMailEvent(ctx, user.ID, message)
	if err != nil {
		t.Fatal(err)
	}
	x, y := elliptic.P256().ScalarBaseMult([]byte{98})
	rotatedP256DH := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), x, y))
	rotatedAuth := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{98}, 16))
	server := &Server{
		store: db,
		webPushSend: func(_ context.Context, _ *http.Client, _ []byte, _ store.WebPushSubscription, _ []byte, _ string) (webPushSendResult, error) {
			if _, err := db.SaveWebPushSubscription(ctx, user.ID, store.WebPushSubscription{
				Endpoint: original.Endpoint,
				P256DH:   rotatedP256DH,
				Auth:     rotatedAuth,
			}); err != nil {
				t.Fatal(err)
			}
			return webPushSendResult{StatusCode: http.StatusGone}, errors.New("old registration expired")
		},
	}
	if retry := server.notifyNewMailWebPush(ctx, user.ID); !retry {
		t.Fatal("stale response for replaced keys did not schedule current subscription work")
	}
	subs, err := db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].P256DH != rotatedP256DH || subs[0].LastNewMailEventID == event.ID {
		t.Fatalf("stale old-key response removed or advanced replacement: %+v", subs)
	}

	server.webPushSend = func(_ context.Context, _ *http.Client, _ []byte, delivered store.WebPushSubscription, _ []byte, _ string) (webPushSendResult, error) {
		if delivered.P256DH != rotatedP256DH || delivered.Auth != rotatedAuth {
			t.Fatal("replacement delivery did not use current key material")
		}
		return webPushSendResult{StatusCode: http.StatusCreated}, nil
	}
	if retry := server.notifyNewMailWebPush(ctx, user.ID); retry {
		t.Fatal("replacement subscription remained pending after successful delivery")
	}
	subs, err = db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].LastNewMailEventID != event.ID {
		t.Fatalf("replacement cursor = %+v, want event %d", subs, event.ID)
	}
}

func TestNewMailWebPushDeletesStaleSubscription(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			user, err := db.CreateUser(ctx, "stale-native-push@example.test", "Stale", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			saveNotifierTestSubscription(t, ctx, db, user.ID, "https://push.example.test/stale")
			message := createNotificationTestMessage(t, ctx, db, user, 901, "Sender", "Gone")
			if _, _, err := db.RecordNewMailEvent(ctx, user.ID, message); err != nil {
				t.Fatal(err)
			}
			server := &Server{
				store: db,
				webPushSend: func(context.Context, *http.Client, []byte, store.WebPushSubscription, []byte, string) (webPushSendResult, error) {
					return webPushSendResult{StatusCode: status}, errors.New("subscription rejected")
				},
			}
			server.notifyNewMailWebPush(ctx, user.ID)
			subs, err := db.ListWebPushSubscriptions(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(subs) != 0 {
				t.Fatalf("stale subscriptions = %+v, want none", subs)
			}
		})
	}
}

func TestNewMailWebPushAsyncCoalescesDirtyUserWithoutLosingEvent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "coalesced-push@example.test", "Coalesced", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	saveNotifierTestSubscription(t, ctx, db, user.ID, "https://push.example.test/coalesced")
	first := createNotificationTestMessage(t, ctx, db, user, 1001, "First", "First")
	if _, _, err := db.RecordNewMailEvent(ctx, user.ID, first); err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var sends atomic.Int32
	server := &Server{
		store: db,
		webPushSend: func(context.Context, *http.Client, []byte, store.WebPushSubscription, []byte, string) (webPushSendResult, error) {
			switch sends.Add(1) {
			case 1:
				close(firstEntered)
				<-releaseFirst
			case 2:
				close(secondEntered)
			}
			return webPushSendResult{StatusCode: http.StatusOK}, nil
		},
	}
	server.notifyNewMailWebPushAsync(user.ID)
	waitPushTestSignal(t, firstEntered, "first delivery")
	second := createNotificationTestMessage(t, ctx, db, user, 1002, "Second", "Second")
	secondEvent, _, err := db.RecordNewMailEvent(ctx, user.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		server.notifyNewMailWebPushAsync(user.ID)
	}
	close(releaseFirst)
	waitPushTestSignal(t, secondEntered, "coalesced delivery")
	server.notifyNewMailWebPush(ctx, user.ID)
	if sends.Load() != 2 {
		t.Fatalf("deliveries = %d, want 2 exact event windows", sends.Load())
	}
	subs, err := db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].LastNewMailEventID != secondEvent.ID {
		t.Fatalf("coalesced cursor = %+v, want event %d", subs, secondEvent.ID)
	}
}

func TestNewMailWebPushAsyncRetriesTransientFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "retry-push@example.test", "Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	saveNotifierTestSubscription(t, ctx, db, user.ID, "https://push.example.test/retry")
	message := createNotificationTestMessage(t, ctx, db, user, 1101, "Retry", "Retry")
	event, _, err := db.RecordNewMailEvent(ctx, user.ID, message)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan struct{})
	var sends atomic.Int32
	server := &Server{
		store: db,
		webPushRetryDelay: func(int) time.Duration {
			return 0
		},
		webPushSend: func(context.Context, *http.Client, []byte, store.WebPushSubscription, []byte, string) (webPushSendResult, error) {
			if sends.Add(1) < 3 {
				return webPushSendResult{StatusCode: http.StatusServiceUnavailable}, errors.New("temporary push failure")
			}
			close(delivered)
			return webPushSendResult{StatusCode: http.StatusOK}, nil
		},
	}
	server.notifyNewMailWebPushAsync(user.ID)
	waitPushTestSignal(t, delivered, "successful retry")
	server.notifyNewMailWebPush(ctx, user.ID)
	if sends.Load() != 3 {
		t.Fatalf("delivery attempts = %d, want 3", sends.Load())
	}
	subs, err := db.ListWebPushSubscriptions(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].LastNewMailEventID != event.ID {
		t.Fatalf("retry cursor = %+v, want event %d", subs, event.ID)
	}
}

func waitPushTestSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func saveNotifierTestSubscription(t *testing.T, ctx context.Context, db *store.Store, userID int64, endpoint string) store.WebPushSubscription {
	t.Helper()
	x, y := elliptic.P256().ScalarBaseMult([]byte{byte(userID%250 + 1)})
	sub, err := db.SaveWebPushSubscription(ctx, userID, store.WebPushSubscription{
		Endpoint: endpoint,
		P256DH:   base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), x, y)),
		Auth:     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(userID%250 + 1)}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

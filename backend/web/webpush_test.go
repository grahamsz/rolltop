package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestWebPushVAPIDKeyDerivationIsStable(t *testing.T) {
	master := []byte("12345678901234567890123456789012")
	first, err := deriveWebPushVAPIDKey(master)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveWebPushVAPIDKey(master)
	if err != nil {
		t.Fatal(err)
	}
	firstPublic := webPushPublicKeyBytes(&first.PublicKey)
	secondPublic := webPushPublicKeyBytes(&second.PublicKey)
	if !reflect.DeepEqual(firstPublic, secondPublic) {
		t.Fatal("derived VAPID public key changed for the same master key")
	}
	encoded := base64.RawURLEncoding.EncodeToString(firstPublic)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 65 || decoded[0] != 0x04 {
		t.Fatalf("decoded public key length/prefix = %d/%#x, want 65/0x04", len(decoded), decoded[0])
	}
}

func TestWebPushVAPIDAuthorizationShape(t *testing.T) {
	key, err := deriveWebPushVAPIDKey([]byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	token, publicKey, err := webPushVAPIDAuthorization(key, "https://push.example.test/send/abc", "user@example.test", time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(publicKey); err != nil || len(decoded) != 65 || decoded[0] != 0x04 {
		t.Fatalf("public key decode len/prefix err = %d/%v", len(decoded), err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "https://push.example.test" || claims["sub"] != "mailto:user@example.test" {
		t.Fatalf("claims = %+v", claims)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature len = %d, want 64", len(sig))
	}
}

func TestEncryptWebPushPayloadBuildsAES128GCMRecord(t *testing.T) {
	receiver, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	message, err := encryptWebPushPayload(webPushPublicKeyBytes(&receiver.PublicKey), auth, []byte(`{"hello":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Salt) != 16 || len(message.PublicKey) != 65 || message.PublicKey[0] != 0x04 {
		t.Fatalf("salt/public key lengths = %d/%d prefix=%#x", len(message.Salt), len(message.PublicKey), message.PublicKey[0])
	}
	if len(message.Body) <= 16+4+1+65+16 {
		t.Fatalf("encrypted body too short: %d", len(message.Body))
	}
	if got := binary.BigEndian.Uint32(message.Body[16:20]); got != webPushRecordSize {
		t.Fatalf("record size = %d, want %d", got, webPushRecordSize)
	}
	if keyLen := int(message.Body[20]); keyLen != 65 || message.Body[21] != 0x04 {
		t.Fatalf("body key len/prefix = %d/%#x", keyLen, message.Body[21])
	}
}

func TestNewMailWebPushNotificationBody(t *testing.T) {
	note := newMailWebPushNotification(3, store.SyncRun{
		LatestNewFrom:    "Alice Example <alice@example.test>",
		LatestNewSubject: "Quarterly update",
	})
	if note.Title != "rolltop - Alice Example" {
		t.Fatalf("title = %q", note.Title)
	}
	if note.Body != "3 new messages synced. Latest: Quarterly update" {
		t.Fatalf("body = %q", note.Body)
	}
	if note.URL != "/mail" || note.Tag == "" {
		t.Fatalf("notification = %+v", note)
	}
}

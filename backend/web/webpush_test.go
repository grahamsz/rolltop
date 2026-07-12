package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
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

func TestSendWebPushUsesHighUrgencyForBackgroundMail(t *testing.T) {
	receiver, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: webPushRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Urgency"); got != webPushNewMailUrgency {
			t.Fatalf("Urgency = %q, want %q", got, webPushNewMailUrgency)
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{}, Body: http.NoBody}, nil
	})}
	_, err = sendWebPush(context.Background(), client, []byte("12345678901234567890123456789012"), store.WebPushSubscription{
		Endpoint: "https://push.example.test/send/high-priority",
		P256DH:   base64.RawURLEncoding.EncodeToString(webPushPublicKeyBytes(&receiver.PublicKey)),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}, []byte(`{"new_mail":true}`), "user@example.test")
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewMailWebPushNotificationBody(t *testing.T) {
	note := newMailWebPushNotification(3, store.NewMailEvent{
		FromAddr:  "Alice Example <alice@example.test>",
		Subject:   "Quarterly update",
		MessageID: 42,
	})
	if note.Title != "rolltop - Alice Example" {
		t.Fatalf("title = %q", note.Title)
	}
	if note.Body != "3 new messages synced. Latest: Quarterly update" {
		t.Fatalf("body = %q", note.Body)
	}
	if note.URL != "/mail" || note.APIURL != "/api/mail?page=1" || note.MessageID != 0 || note.Tag == "" {
		t.Fatalf("notification = %+v", note)
	}
}

func TestSingleNewMailWebPushDeepLinksAndPrefetchesWithoutOpening(t *testing.T) {
	note := newMailWebPushNotification(1, store.NewMailEvent{MessageID: 42})
	if note.URL != "/messages/42?back=%2Fmail" || note.APIURL != "/api/messages/42/prefetch" || note.MessageID != 42 {
		t.Fatalf("notification = %+v", note)
	}
}

func TestPublicWebPushDialerOnlyDialsResolvedPublicAddresses(t *testing.T) {
	resolver := staticWebPushResolver{addresses: []netip.Addr{
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("10.0.0.5"),
		netip.MustParseAddr("8.8.8.8"),
	}}
	dialer := &recordingWebPushDialer{err: errors.New("stop after validation")}
	publicDialer := &publicWebPushDialer{resolver: resolver, dialer: dialer}
	if _, err := publicDialer.DialContext(context.Background(), "tcp", "push.example.com:443"); err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if !reflect.DeepEqual(dialer.addresses, []string{"8.8.8.8:443"}) {
		t.Fatalf("dialed addresses = %v, want only the resolved public address", dialer.addresses)
	}
}

func TestPublicWebPushDialerRejectsPrivateOnlyResolution(t *testing.T) {
	dialer := &recordingWebPushDialer{}
	publicDialer := &publicWebPushDialer{
		resolver: staticWebPushResolver{addresses: []netip.Addr{
			netip.MustParseAddr("169.254.169.254"),
			netip.MustParseAddr("fd00::1"),
			netip.MustParseAddr("64:ff9b::a00:1"),
		}},
		dialer: dialer,
	}
	if _, err := publicDialer.DialContext(context.Background(), "tcp", "push.example.com:443"); err == nil {
		t.Fatal("private-only endpoint resolution was accepted")
	}
	if len(dialer.addresses) != 0 {
		t.Fatalf("private addresses reached network dialer: %v", dialer.addresses)
	}
}

func TestWebPushHTTPClientDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	client := newWebPushHTTPClient()
	client.Transport = webPushRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://redirect.example.com/private"}},
			Body:       http.NoBody,
		}, nil
	})
	response, err := client.Get("https://push.example.com/send")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound || requests != 1 {
		t.Fatalf("redirect response status/requests = %d/%d, want 302/1", response.StatusCode, requests)
	}
}

type staticWebPushResolver struct {
	addresses []netip.Addr
	err       error
}

func (r staticWebPushResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

type recordingWebPushDialer struct {
	addresses []string
	err       error
}

func (d *recordingWebPushDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	return nil, d.err
}

type webPushRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn webPushRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

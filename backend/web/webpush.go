// File overview: VAPID signing and RFC 8291 aes128gcm Web Push encryption.

package web

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"

	"rolltop/backend/store"
)

const (
	webPushVAPIDLabel       = "rolltop webpush vapid v1"
	webPushRecordSize       = 4096
	webPushDefaultTTL       = 24 * time.Hour
	webPushHTTPTimeout      = 10 * time.Second
	webPushInvalidStatusMin = 400
	webPushNewMailUrgency   = "high"
)

type webPushSendResult struct {
	StatusCode int
}

type webPushSendFunc func(context.Context, *http.Client, []byte, store.WebPushSubscription, []byte, string) (webPushSendResult, error)

type webPushEncryptedMessage struct {
	Body      []byte
	PublicKey []byte
	Salt      []byte
}

func (s *Server) webPushVAPIDPublicKey() (string, error) {
	key, err := deriveWebPushVAPIDKey(s.masterKey)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(webPushPublicKeyBytes(&key.PublicKey)), nil
}

func sendWebPush(ctx context.Context, client *http.Client, masterKey []byte, sub store.WebPushSubscription, payload []byte, subject string) (webPushSendResult, error) {
	endpoint := strings.TrimSpace(sub.Endpoint)
	if endpoint == "" {
		return webPushSendResult{}, errors.New("web push endpoint is required")
	}
	receiverPublicKey, err := decodeBase64URL(sub.P256DH)
	if err != nil {
		return webPushSendResult{}, fmt.Errorf("decode subscription p256dh: %w", err)
	}
	authSecret, err := decodeBase64URL(sub.Auth)
	if err != nil {
		return webPushSendResult{}, fmt.Errorf("decode subscription auth: %w", err)
	}
	privateKey, err := deriveWebPushVAPIDKey(masterKey)
	if err != nil {
		return webPushSendResult{}, err
	}
	encrypted, err := encryptWebPushPayload(receiverPublicKey, authSecret, payload)
	if err != nil {
		return webPushSendResult{}, err
	}
	vapidToken, vapidPublicKey, err := webPushVAPIDAuthorization(privateKey, endpoint, subject, time.Now().UTC())
	if err != nil {
		return webPushSendResult{}, err
	}
	if client == nil {
		client = newWebPushHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encrypted.Body))
	if err != nil {
		return webPushSendResult{}, err
	}
	req.Header.Set("Authorization", "vapid t="+vapidToken+", k="+vapidPublicKey)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Crypto-Key", "p256ecdsa="+vapidPublicKey)
	req.Header.Set("TTL", fmt.Sprintf("%.0f", webPushDefaultTTL.Seconds()))
	// New-mail pushes immediately produce a user-visible notification, so high
	// urgency is appropriate and lets FCM wake devices that are in Doze.
	req.Header.Set("Urgency", webPushNewMailUrgency)
	res, err := client.Do(req)
	if err != nil {
		return webPushSendResult{}, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	result := webPushSendResult{StatusCode: res.StatusCode}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return result, nil
	}
	return result, fmt.Errorf("web push service returned %d", res.StatusCode)
}

func newWebPushHTTPClient() *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if defaults, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaults.Clone()
	}
	transport.Proxy = nil
	transport.DialContext = (&publicWebPushDialer{
		resolver: net.DefaultResolver,
		dialer: &net.Dialer{
			Timeout:   webPushHTTPTimeout,
			KeepAlive: 30 * time.Second,
		},
	}).DialContext
	return &http.Client{
		Transport: transport,
		Timeout:   webPushHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type publicWebPushDialer struct {
	resolver webPushResolver
	dialer   webPushNetworkDialer
}

type webPushResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type webPushNetworkDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

var nonPublicWebPushDialPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func (d *publicWebPushDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid web push address: %w", err)
	}
	resolver := d.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := d.dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: webPushHTTPTimeout}
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve web push endpoint: %w", err)
	}
	var lastErr error
	for _, addr := range addresses {
		addr = addr.Unmap()
		if !isPublicWebPushAddress(addr) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("connect to web push endpoint: %w", lastErr)
	}
	return nil, errors.New("web push endpoint did not resolve to a public address")
}

func isPublicWebPushAddress(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicWebPushDialPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func deriveWebPushVAPIDKey(masterKey []byte) (*ecdsa.PrivateKey, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("web push requires a 32 byte master key")
	}
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(webPushVAPIDLabel))
	seed := mac.Sum(nil)
	curve := elliptic.P256()
	n := curve.Params().N
	d := new(big.Int).SetBytes(seed)
	d.Mod(d, new(big.Int).Sub(n, big.NewInt(1)))
	d.Add(d, big.NewInt(1))
	x, y := curve.ScalarBaseMult(leftPadBytes(d.Bytes(), 32))
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
		D:         d,
	}, nil
}

func webPushVAPIDAuthorization(privateKey *ecdsa.PrivateKey, endpoint string, subject string, now time.Time) (string, string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", errors.New("invalid web push endpoint")
	}
	aud := parsed.Scheme + "://" + parsed.Host
	sub := strings.TrimSpace(subject)
	if sub == "" || !strings.Contains(sub, "@") {
		sub = "rolltop@localhost"
	}
	if !strings.HasPrefix(sub, "mailto:") && !strings.HasPrefix(sub, "https://") && !strings.HasPrefix(sub, "http://") {
		sub = "mailto:" + sub
	}
	header := map[string]string{"typ": "JWT", "alg": "ES256"}
	claims := map[string]any{
		"aud": aud,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": sub,
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", "", err
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		return "", "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, digest[:])
	if err != nil {
		return "", "", err
	}
	sig := append(leftPadBytes(r.Bytes(), 32), leftPadBytes(s.Bytes(), 32)...)
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	publicKey := base64.RawURLEncoding.EncodeToString(webPushPublicKeyBytes(&privateKey.PublicKey))
	return token, publicKey, nil
}

func encryptWebPushPayload(receiverPublicKey []byte, authSecret []byte, payload []byte) (webPushEncryptedMessage, error) {
	curve := elliptic.P256()
	rx, ry := elliptic.Unmarshal(curve, receiverPublicKey)
	if rx == nil || ry == nil {
		return webPushEncryptedMessage{}, errors.New("invalid receiver public key")
	}
	ephemeral, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return webPushEncryptedMessage{}, err
	}
	senderPublicKey := webPushPublicKeyBytes(&ephemeral.PublicKey)
	sx, _ := curve.ScalarMult(rx, ry, leftPadBytes(ephemeral.D.Bytes(), 32))
	if sx == nil {
		return webPushEncryptedMessage{}, errors.New("failed to derive web push shared secret")
	}
	sharedSecret := leftPadBytes(sx.Bytes(), 32)
	keyInfo := append([]byte("WebPush: info\x00"), receiverPublicKey...)
	keyInfo = append(keyInfo, senderPublicKey...)
	ikm := hkdfBytes(sha256.New, sharedSecret, authSecret, keyInfo, 32)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return webPushEncryptedMessage{}, err
	}
	cek := hkdfBytes(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfBytes(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return webPushEncryptedMessage{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return webPushEncryptedMessage{}, err
	}
	record := append(append([]byte{}, payload...), 0x02)
	ciphertext := aead.Seal(nil, nonce, record, nil)
	body := make([]byte, 0, 16+4+1+len(senderPublicKey)+len(ciphertext))
	body = append(body, salt...)
	var rs [4]byte
	binary.BigEndian.PutUint32(rs[:], webPushRecordSize)
	body = append(body, rs[:]...)
	body = append(body, byte(len(senderPublicKey)))
	body = append(body, senderPublicKey...)
	body = append(body, ciphertext...)
	return webPushEncryptedMessage{Body: body, PublicKey: senderPublicKey, Salt: salt}, nil
}

func hkdfBytes(hashFn func() hash.Hash, secret []byte, salt []byte, info []byte, length int) []byte {
	out := make([]byte, length)
	_, _ = io.ReadFull(hkdf.New(hashFn, secret, salt, info), out)
	return out
}

func webPushPublicKeyBytes(key *ecdsa.PublicKey) []byte {
	return elliptic.Marshal(elliptic.P256(), key.X, key.Y)
}

func leftPadBytes(in []byte, size int) []byte {
	if len(in) >= size {
		return in
	}
	out := make([]byte, size)
	copy(out[size-len(in):], in)
	return out
}

func decodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty base64 value")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

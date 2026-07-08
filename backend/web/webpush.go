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
	"net/http"
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
)

type webPushSendResult struct {
	StatusCode int
}

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
		client = &http.Client{Timeout: webPushHTTPTimeout}
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
	req.Header.Set("Urgency", "normal")
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

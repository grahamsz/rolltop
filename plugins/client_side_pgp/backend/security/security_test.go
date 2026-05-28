package security

import (
	"strings"
	"testing"

	"rolltop/backend/plugins"
)

func TestDetectsAndStripsInlinePGPSignedBody(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: signed text",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"-----BEGIN PGP SIGNED MESSAGE-----",
		"Hash: SHA512",
		"",
		"This is a signed message",
		"-----BEGIN PGP SIGNATURE-----",
		"",
		"wrfakebase64",
		"-----END PGP SIGNATURE-----",
	}, "\r\n")
	bodyText := strings.Join([]string{
		"-----BEGIN PGP SIGNED MESSAGE-----",
		"Hash: SHA512",
		"",
		"This is a signed message",
		"-----BEGIN PGP SIGNATURE-----",
		"",
		"wrfakebase64",
		"-----END PGP SIGNATURE-----",
	}, "\r\n")

	state := Detect([]byte(raw), plugins.MessageBody{})
	if state.Encrypted || !state.Signed {
		t.Fatalf("state = %+v", state)
	}
	transform := Transform([]byte(raw), state, plugins.MessageBody{Purpose: "storage", Text: bodyText})
	if !transform.Applied {
		t.Fatal("signed body transform was not applied")
	}
	if transform.Body.Text != "This is a signed message" {
		t.Fatalf("text = %q", transform.Body.Text)
	}
	if strings.Contains(transform.Body.Text, "BEGIN PGP") || strings.Contains(transform.Body.Text, "SIGNATURE") {
		t.Fatalf("signature armor was retained: %q", transform.Body.Text)
	}
}

func TestInlinePGPEncryptedTransformsStorageAndDisplay(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: encrypted text",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"-----BEGIN PGP MESSAGE-----",
		"",
		"wcDMA123",
		"-----END PGP MESSAGE-----",
	}, "\r\n")

	state := Detect([]byte(raw), plugins.MessageBody{})
	if !state.Encrypted || state.Signed {
		t.Fatalf("state = %+v", state)
	}
	storage := Transform([]byte(raw), state, plugins.MessageBody{Purpose: "storage", Text: raw})
	if !storage.Applied || !storage.DropAttachments || storage.Body.Text != "" || storage.Body.HTML != "" {
		t.Fatalf("storage transform = %+v", storage)
	}
	display := Transform([]byte(raw), state, plugins.MessageBody{Purpose: "display"})
	if !display.Applied || display.Body.HTML != "" {
		t.Fatalf("display transform = %+v", display)
	}
	if !strings.Contains(display.Body.Text, "-----BEGIN PGP MESSAGE-----") || !strings.Contains(display.Body.Text, "wcDMA123") {
		t.Fatalf("display text did not keep ciphertext: %q", display.Body.Text)
	}
}

func TestPGPMIMEEncryptedDisplayShowsCiphertextPart(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: encrypted mime",
		"MIME-Version: 1.0",
		`Content-Type: multipart/encrypted; protocol="application/pgp-encrypted"; boundary="pgp-boundary"`,
		"",
		"--pgp-boundary",
		"Content-Type: application/pgp-encrypted",
		"",
		"Version: 1",
		"--pgp-boundary",
		`Content-Type: application/octet-stream; name="encrypted.asc"`,
		`Content-Disposition: inline; filename="encrypted.asc"`,
		"Content-Transfer-Encoding: 7bit",
		"",
		"-----BEGIN PGP MESSAGE-----",
		"",
		"wcDMA456",
		"-----END PGP MESSAGE-----",
		"--pgp-boundary--",
	}, "\r\n")

	state := Detect([]byte(raw), plugins.MessageBody{})
	if !state.Encrypted || state.Signed {
		t.Fatalf("state = %+v", state)
	}
	display := Transform([]byte(raw), state, plugins.MessageBody{Purpose: "display"})
	if !display.Applied || display.Body.HTML != "" {
		t.Fatalf("display transform = %+v", display)
	}
	if !strings.Contains(display.Body.Text, "-----BEGIN PGP MESSAGE-----") || !strings.Contains(display.Body.Text, "wcDMA456") {
		t.Fatalf("display text did not keep PGP/MIME ciphertext: %q", display.Body.Text)
	}
	if strings.Contains(display.Body.Text, "Version: 1") {
		t.Fatalf("display text included PGP/MIME version part: %q", display.Body.Text)
	}
}

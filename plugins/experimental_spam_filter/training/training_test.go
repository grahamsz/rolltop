package training

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

func TestExtractTarRejectsTraversal(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTar(tar.NewReader(bytes.NewReader(archive.Bytes())), t.TempDir()); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestDownloadOneVerifiesChecksum(t *testing.T) {
	payload := []byte("pinned corpus bytes")
	digest := sha256.Sum256(payload)
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Header:     make(http.Header),
		}, nil
	})}
	destination := filepath.Join(t.TempDir(), "archive.tar.bz2")
	spec := CorpusSpec{Name: "archive.tar.bz2", URL: "https://corpus.invalid/archive.tar.bz2", SHA256: hex.EncodeToString(digest[:]), Label: "ham"}
	if err := downloadOne(context.Background(), client, spec, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("download mismatch: %q, %v", got, err)
	}
	spec.SHA256 = strings.Repeat("0", 64)
	if err := downloadOne(context.Background(), client, spec, destination); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestFTRLTrainingDeterministic(t *testing.T) {
	samples := []sample{
		{Spam: true, Message: spammodel.Message{Subject: "free cash prize", Body: "click now winner"}},
		{Spam: true, Message: spammodel.Message{Subject: "limited offer", Body: "buy cheap pills"}},
		{Spam: false, Message: spammodel.Message{Subject: "meeting notes", Body: "project review Friday"}},
		{Spam: false, Message: spammodel.Message{Subject: "family dinner", Body: "see you tomorrow"}},
	}
	firstWeights, firstBias, err := trainLogistic(samples, 1024)
	if err != nil {
		t.Fatal(err)
	}
	secondWeights, secondBias, err := trainLogistic(samples, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if firstBias != secondBias {
		t.Fatalf("bias is not deterministic: %v != %v", firstBias, secondBias)
	}
	for index := range firstWeights {
		if firstWeights[index] != secondWeights[index] {
			t.Fatalf("weight %d is not deterministic", index)
		}
	}
}

func TestParseCorpusMessageUsesRuntimeMIMESemantics(t *testing.T) {
	raw := strings.Join([]string{
		"From: =?UTF-8?Q?Example_Sender?= <sender@example.test>",
		"To: One <one@example.test>",
		"Cc: two@example.test",
		"Subject: =?UTF-8?Q?Decoded_Subject?=",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="training-boundary"`,
		"",
		"--training-boundary",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: base64",
		"",
		"RGVjb2RlZCB0cmFpbmluZyBib2R5Lg==",
		"--training-boundary",
		`Content-Type: application/octet-stream; name="offer.bin"`,
		"Content-Transfer-Encoding: base64",
		`Content-Disposition: attachment; filename="offer.bin"`,
		"",
		"YXR0YWNobWVudC1vbmx5LXNwYW0tdG9rZW4=",
		"--training-boundary--",
		"",
	}, "\r\n")
	message, err := parseCorpusMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if message.Subject != "Decoded Subject" || message.From != `"Example Sender" <sender@example.test>` {
		t.Fatalf("decoded headers = subject %q from %q", message.Subject, message.From)
	}
	if message.Body != "Decoded training body." {
		t.Fatalf("decoded body = %q", message.Body)
	}
	if strings.Contains(message.Body, "training-boundary") || strings.Contains(message.Body, "attachment-only-spam-token") {
		t.Fatalf("MIME or attachment content leaked into body features: %q", message.Body)
	}
	if len(message.To) != 2 || message.To[0] != "one@example.test" || message.To[1] != "two@example.test" {
		t.Fatalf("recipients = %#v", message.To)
	}
	if len(message.AttachmentTypes) != 1 || message.AttachmentTypes[0] != "application/octet-stream" {
		t.Fatalf("attachment metadata = %#v", message.AttachmentTypes)
	}
}

func TestCorpusCacheIgnoredAndArchivesUntracked(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	ignore, err := os.ReadFile(filepath.Join(repositoryRoot, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), ".cache/rolltop-spam-corpus/") {
		t.Fatal("corpus cache is not ignored")
	}
	command := exec.Command("git", "ls-files")
	command.Dir = repositoryRoot
	tracked, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range CorpusSpecs {
		for _, path := range strings.Split(string(tracked), "\n") {
			if filepath.Base(path) == spec.Name || strings.HasPrefix(path, ".cache/rolltop-spam-corpus/") {
				t.Fatalf("raw corpus path is tracked: %s", path)
			}
		}
	}
}

func TestCheckedInArtifactsVerifyOffline(t *testing.T) {
	modelDir := filepath.Join("..", "model")
	if err := Verify(ArtifactPaths{
		Model:    filepath.Join(modelDir, "model.bin"),
		Metadata: filepath.Join(modelDir, "model.json"),
		Report:   filepath.Join(modelDir, "benchmark.json"),
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

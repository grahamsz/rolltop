package training

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

func TestNamedRulePerceptronTrainingDeterministic(t *testing.T) {
	var samples []sample
	for index := 0; index < 8; index++ {
		samples = append(samples,
			sample{ID: fmt.Sprintf("spam-%d", index), Spam: true, Message: spammodel.Message{Subject: "claim your cash prize", Body: "Click here to claim your prize. Act now.", From: "offers@example.test", To: []string{"reader@example.test"}, MIMEType: "text/html", HTML: true}},
			sample{ID: fmt.Sprintf("ham-%d", index), Spam: false, Message: spammodel.Message{Subject: "meeting notes", Body: "Please review the project plan and action items. Best regards.", From: "alex@example.test", To: []string{"team@example.test"}, MIMEType: "text/plain"}},
		)
	}
	hits, err := massCheckSamples(samples)
	if err != nil {
		t.Fatal(err)
	}
	audits, ranges, err := auditRuleHits(hits)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	for _, audit := range audits {
		if audit.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		t.Fatal("expected enabled authored rules")
	}
	firstWeights, firstBias, err := trainPerceptron(hits, ranges, DefaultPerceptronConfig)
	if err != nil {
		t.Fatal(err)
	}
	secondWeights, secondBias, err := trainPerceptron(hits, ranges, DefaultPerceptronConfig)
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

func TestValidationSelectedOperatingPointDoesNotTuneOnTest(t *testing.T) {
	validationProbabilities := []float64{.99, .80, .70, .20, .10}
	validationLabels := []bool{true, true, false, false, false}
	testProbabilities := []float64{.95, .79, .78, .30, .20}
	testLabels := []bool{true, true, false, false, false}
	point, err := validationSelectedOperatingPoint(validationProbabilities, validationLabels, testProbabilities, testLabels, 0)
	if err != nil {
		t.Fatal(err)
	}
	if point.Threshold != .80 {
		t.Fatalf("validation threshold = %v, want .80", point.Threshold)
	}
	if point.TestRecall != .5 || point.TestFalsePositiveRate != 0 {
		t.Fatalf("held-out metrics = recall %v fpr %v, want .5 and 0", point.TestRecall, point.TestFalsePositiveRate)
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
	reportPath := filepath.Join(modelDir, "benchmark.json")
	if err := Verify(ArtifactPaths{
		Model:    filepath.Join(modelDir, "model.bin"),
		Metadata: filepath.Join(modelDir, "model.json"),
		Report:   reportPath,
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, machineDependentField := range []string{
		`"runtime":`, `"inference_nanoseconds_per_message"`, `"allocated_bytes_per_message"`,
	} {
		if bytes.Contains(report, []byte(machineDependentField)) {
			t.Fatalf("checksum-bound report contains machine-dependent field %s", machineDependentField)
		}
	}
}

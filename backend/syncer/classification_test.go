package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rolltop/backend/mailparse"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

type captureMessageClassifier struct {
	input plugins.MessageClassificationInput
	err   error
	calls int
}

func (*captureMessageClassifier) ID() string { return "capture_classifier" }

func (*captureMessageClassifier) Start(plugins.BackendStartHost) error { return nil }

func (*captureMessageClassifier) Stop(plugins.BackendStartHost) error { return nil }

func (c *captureMessageClassifier) ClassifyMessage(_ context.Context, _ plugins.MessageClassificationHost, input plugins.MessageClassificationInput) error {
	c.input = input
	c.calls++
	return c.err
}

func TestMessageClassificationBatchDefersAndBoundsBody(t *testing.T) {
	hook := &captureMessageClassifier{}
	service := &Service{}
	batch := newMessageClassificationBatch(service, []plugins.MessageClassifier{hook})
	body := "first\n" + string(make([]byte, maxQueuedClassificationBodyBytes+200))
	batch.Add(store.MessageRecord{ID: 3, UserID: 2}, mailparse.ParsedMessage{Text: body})
	if hook.calls != 0 {
		t.Fatalf("classifier ran before post-index batch flush")
	}
	batch.Flush(context.Background())
	if hook.calls != 1 {
		t.Fatalf("classifier calls = %d, want 1", hook.calls)
	}
	if !hook.input.BodyTruncated || len(hook.input.BodyText) > maxQueuedClassificationBodyBytes {
		t.Fatalf("bounded body len=%d truncated=%t", len(hook.input.BodyText), hook.input.BodyTruncated)
	}
	if !strings.HasPrefix(hook.input.BodyText, "first\n") {
		t.Fatalf("bounded body did not preserve the model's prefix semantics: %q", hook.input.BodyText[:min(len(hook.input.BodyText), 16)])
	}
}

func TestClassifyStoredMessagePassesMetadataAndSuppressesEncryptedBody(t *testing.T) {
	hook := &captureMessageClassifier{err: errors.New("advisory failure")}
	service := &Service{}
	service.classifyStoredMessage(context.Background(), []plugins.MessageClassifier{hook}, store.MessageRecord{
		ID:          17,
		UserID:      9,
		AccountID:   4,
		MailboxID:   6,
		Subject:     "Encrypted offer",
		FromAddr:    "sender@example.test",
		IsEncrypted: true,
	}, mailparse.ParsedMessage{
		Text:  "secret plaintext",
		HTML:  "<p>secret plaintext</p>",
		Files: []mailparse.Attachment{{Filename: "offer.pdf", ContentType: "application/pdf", Data: []byte("bytes")}},
	})

	if hook.input.UserID != 9 || hook.input.MessageID != 17 {
		t.Fatalf("tenant/message input = %#v", hook.input)
	}
	if hook.input.BodyText != "" {
		t.Fatalf("encrypted body leaked to classifier: %q", hook.input.BodyText)
	}
	if len(hook.input.Attachments) != 1 || hook.input.Attachments[0].Size != 5 {
		t.Fatalf("attachment metadata = %#v", hook.input.Attachments)
	}
}

package imapclient

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/responses"

	"rolltop/backend/syncer"
)

type appendExecutorFunc func(imap.Commander, responses.Handler) (*imap.StatusResp, error)

func (f appendExecutorFunc) Execute(command imap.Commander, handler responses.Handler) (*imap.StatusResp, error) {
	return f(command, handler)
}

func TestExecuteAppendDistinguishesDefinitiveAndUnknownOutcomes(t *testing.T) {
	remoteErr := errors.New("connection reset")
	_, unknown := executeAppend(appendExecutorFunc(func(imap.Commander, responses.Handler) (*imap.StatusResp, error) {
		return nil, remoteErr
	}), "INBOX", nil, time.Time{}, bytes.NewReader([]byte("message")))
	if !errors.Is(unknown, remoteErr) || !syncer.IsAppendOutcomeUnknown(unknown) {
		t.Fatalf("unknown error = %v", unknown)
	}

	_, definitive := executeAppend(appendExecutorFunc(func(command imap.Commander, _ responses.Handler) (*imap.StatusResp, error) {
		if command.Command().Name != "APPEND" {
			t.Fatalf("command = %q", command.Command().Name)
		}
		return &imap.StatusResp{Type: imap.StatusRespNo, Info: "mailbox rejected append"}, nil
	}), "INBOX", nil, time.Time{}, bytes.NewReader([]byte("message")))
	if definitive == nil || syncer.IsAppendOutcomeUnknown(definitive) {
		t.Fatalf("definitive error = %v", definitive)
	}

	if _, err := executeAppend(appendExecutorFunc(func(imap.Commander, responses.Handler) (*imap.StatusResp, error) {
		return &imap.StatusResp{Type: imap.StatusRespOk}, nil
	}), "INBOX", nil, time.Time{}, bytes.NewReader([]byte("message"))); err != nil {
		t.Fatalf("successful append: %v", err)
	}
}

func TestExecuteAppendReturnsAPPENDUIDReceipt(t *testing.T) {
	receipt, err := executeAppend(appendExecutorFunc(func(imap.Commander, responses.Handler) (*imap.StatusResp, error) {
		return &imap.StatusResp{
			Type:      imap.StatusRespOk,
			Code:      appendUIDResponseCode,
			Arguments: []interface{}{"830", "109"},
		}, nil
	}), "INBOX", nil, time.Time{}, bytes.NewReader([]byte("message")))
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.UIDValidity != 830 || receipt.UID != 109 {
		t.Fatalf("receipt = %+v, want UIDVALIDITY 830 UID 109", receipt)
	}
}

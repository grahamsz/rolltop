package imapclient

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/responses"

	"rolltop/backend/syncer"
)

func TestMoveMessageWithReceiptExecutesDirectUIDMove(t *testing.T) {
	client := &fakeMoveCommandClient{
		moveSupported: true,
		status: &imap.StatusResp{
			Type:      imap.StatusRespOk,
			Code:      copyUIDResponseCode,
			Arguments: []interface{}{"830", "42", "109"},
		},
	}
	receipt, err := moveMessageWithReceipt(context.Background(), client, " Spam ", " Inbox ", 42, 830)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.DestinationUIDValidity != 830 || receipt.DestinationUID != 109 {
		t.Fatalf("receipt = %+v, want UIDVALIDITY 830 UID 109", receipt)
	}
	if client.selected != "Spam" || client.readOnly {
		t.Fatalf("selected mailbox = %q readOnly=%t, want Spam read-write", client.selected, client.readOnly)
	}
	if !reflect.DeepEqual(client.supportCalls, []string{"MOVE"}) {
		t.Fatalf("Support calls = %#v, want MOVE", client.supportCalls)
	}
	if client.criteria == nil || client.criteria.Uid == nil || client.criteria.Uid.String() != "42" {
		t.Fatalf("source UID criteria = %+v, want exact UID 42", client.criteria)
	}
	if client.command == nil {
		t.Fatal("UID MOVE command was not executed")
	}
	command := client.command.Command()
	if command.Name != "UID" || len(command.Arguments) != 3 {
		t.Fatalf("command = %+v, want UID MOVE with two arguments", command)
	}
	if got, ok := command.Arguments[0].(imap.RawString); !ok || string(got) != "MOVE" {
		t.Fatalf("UID command name = %#v, want MOVE", command.Arguments[0])
	}
	seqset, ok := command.Arguments[1].(*imap.SeqSet)
	if !ok || seqset.String() != "42" {
		t.Fatalf("UID sequence set = %#v, want 42", command.Arguments[1])
	}
	if got, ok := command.Arguments[2].(string); !ok || got != "Inbox" {
		t.Fatalf("destination = %#v, want Inbox", command.Arguments[2])
	}
}

func TestMoveMessageWithReceiptDoesNotEmulateUnsupportedMove(t *testing.T) {
	client := &fakeMoveCommandClient{}
	receipt, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830)
	if err == nil || !strings.Contains(err.Error(), "will not emulate") {
		t.Fatalf("error = %v, want refusal to emulate MOVE", err)
	}
	if receipt != nil || client.command != nil {
		t.Fatalf("unsupported MOVE executed command or returned receipt: %+v %#v", receipt, client.command)
	}
}

func TestMoveMessageWithReceiptKeepsSuccessWhenReceiptIsUnavailable(t *testing.T) {
	for name, status := range map[string]*imap.StatusResp{
		"absent": {
			Type: imap.StatusRespOk,
		},
		"malformed": {
			Type:      imap.StatusRespOk,
			Code:      copyUIDResponseCode,
			Arguments: []interface{}{"invalid", "42", "109"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeMoveCommandClient{moveSupported: true, status: status}
			receipt, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830)
			if err != nil {
				t.Fatalf("successful MOVE returned error: %v", err)
			}
			if receipt != nil {
				t.Fatalf("receipt = %+v, want nil", receipt)
			}
			if client.command == nil {
				t.Fatal("MOVE command was not executed")
			}
		})
	}
}

func TestMoveMessageWithReceiptReturnsCommandErrors(t *testing.T) {
	want := errors.New("connection failed")
	client := &fakeMoveCommandClient{moveSupported: true, executeErr: want}
	if _, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830); !errors.Is(err, want) || !syncer.IsMoveOutcomeUnknown(err) {
		t.Fatalf("network error = %v, want outcome-unknown wrapping %v", err, want)
	}

	client = &fakeMoveCommandClient{moveSupported: true, status: &imap.StatusResp{Type: imap.StatusRespNo, Info: "permission denied"}}
	if _, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830); err == nil || !strings.Contains(err.Error(), "permission denied") || syncer.IsMoveOutcomeUnknown(err) {
		t.Fatalf("NO response error = %v", err)
	}

	client = &fakeMoveCommandClient{moveSupported: true}
	if _, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830); err == nil || !syncer.IsMoveOutcomeUnknown(err) {
		t.Fatalf("nil tagged response error = %v, want outcome unknown", err)
	}
}

func TestMoveMessageWithReceiptKeepsPreDispatchErrorsDefinitive(t *testing.T) {
	for name, client := range map[string]*fakeMoveCommandClient{
		"select":     {moveSupported: true, selectErr: errors.New("select failed")},
		"capability": {supportErr: errors.New("capability failed")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830)
			if err == nil || syncer.IsMoveOutcomeUnknown(err) {
				t.Fatalf("pre-dispatch error = %v, want definitive failure", err)
			}
			if client.command != nil {
				t.Fatal("MOVE command executed after pre-dispatch failure")
			}
		})
	}
}

func TestMoveMessageWithReceiptRejectsSourceGenerationResetBeforeDispatch(t *testing.T) {
	client := &fakeMoveCommandClient{
		moveSupported:  true,
		selectedStatus: &imap.MailboxStatus{UidValidity: 831},
	}
	_, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830)
	if err == nil || !strings.Contains(err.Error(), "UIDVALIDITY") {
		t.Fatalf("generation reset error = %v, want UIDVALIDITY rejection", err)
	}
	if client.criteria != nil || client.command != nil || len(client.supportCalls) != 0 {
		t.Fatalf("generation reset reached search/dispatch: criteria=%+v command=%#v support=%v",
			client.criteria, client.command, client.supportCalls)
	}
}

func TestMoveMessageWithReceiptRejectsAbsentSourceUIDBeforeDispatch(t *testing.T) {
	client := &fakeMoveCommandClient{moveSupported: true, sourceUIDMissing: true}
	_, err := moveMessageWithReceipt(context.Background(), client, "Spam", "Inbox", 42, 830)
	if err == nil || !strings.Contains(err.Error(), "no longer contains UID 42") {
		t.Fatalf("absent source error = %v, want exact UID rejection", err)
	}
	if client.criteria == nil || client.criteria.Uid == nil || client.criteria.Uid.String() != "42" {
		t.Fatalf("absent source criteria = %+v, want exact UID 42", client.criteria)
	}
	if client.command != nil || len(client.supportCalls) != 0 {
		t.Fatalf("absent source dispatched MOVE: command=%#v support=%v", client.command, client.supportCalls)
	}
}

func TestParseMoveReceiptAcceptsCOPYUID(t *testing.T) {
	for name, args := range map[string][]interface{}{
		"strings":     {"830", "42", "109"},
		"raw strings": {imap.RawString("830"), imap.RawString("42"), imap.RawString("109")},
	} {
		t.Run(name, func(t *testing.T) {
			receipt := parseMoveReceipt(&imap.StatusResp{Code: "copyuid", Arguments: args}, 42)
			if receipt == nil || receipt.DestinationUIDValidity != 830 || receipt.DestinationUID != 109 {
				t.Fatalf("receipt = %+v, want UIDVALIDITY 830 UID 109", receipt)
			}
		})
	}
}

func TestParseMoveReceiptFromTaggedIMAPResponse(t *testing.T) {
	resp, err := imap.ReadResp(imap.NewReader(bytes.NewBufferString("A004 OK [COPYUID 830 42 109] MOVE completed\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	status, ok := resp.(*imap.StatusResp)
	if !ok {
		t.Fatalf("response type = %T, want *imap.StatusResp", resp)
	}
	receipt := parseMoveReceipt(status, 42)
	if receipt == nil || receipt.DestinationUIDValidity != 830 || receipt.DestinationUID != 109 {
		t.Fatalf("wire receipt = %+v, want UIDVALIDITY 830 UID 109", receipt)
	}
}

func TestParseMoveReceiptTreatsAbsentOrMalformedCOPYUIDAsUnavailable(t *testing.T) {
	for name, status := range map[string]*imap.StatusResp{
		"nil status":              nil,
		"absent":                  {Type: imap.StatusRespOk},
		"wrong argument count":    {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42"}},
		"extra argument":          {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42", "109", "extra"}},
		"non-string argument":     {Code: copyUIDResponseCode, Arguments: []interface{}{uint32(830), "42", "109"}},
		"zero UIDVALIDITY":        {Code: copyUIDResponseCode, Arguments: []interface{}{"0", "42", "109"}},
		"invalid UIDVALIDITY":     {Code: copyUIDResponseCode, Arguments: []interface{}{"no", "42", "109"}},
		"overflow UIDVALIDITY":    {Code: copyUIDResponseCode, Arguments: []interface{}{"4294967296", "42", "109"}},
		"wrong source UID":        {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "43", "109"}},
		"source UID range":        {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42:43", "109:110"}},
		"source UID list":         {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "41,42", "108,109"}},
		"dynamic source UID":      {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42:*", "109"}},
		"invalid destination UID": {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42", "no"}},
		"destination UID range":   {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42", "109:110"}},
		"dynamic destination UID": {Code: copyUIDResponseCode, Arguments: []interface{}{"830", "42", "*"}},
	} {
		t.Run(name, func(t *testing.T) {
			if receipt := parseMoveReceipt(status, 42); receipt != nil {
				t.Fatalf("receipt = %+v, want nil", receipt)
			}
		})
	}
}

type fakeMoveCommandClient struct {
	moveSupported    bool
	selectErr        error
	selectedStatus   *imap.MailboxStatus
	sourceUIDMissing bool
	searchErr        error
	supportErr       error
	status           *imap.StatusResp
	executeErr       error
	selected         string
	readOnly         bool
	criteria         *imap.SearchCriteria
	supportCalls     []string
	command          imap.Commander
}

func (f *fakeMoveCommandClient) Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error) {
	f.selected = mailbox
	f.readOnly = readOnly
	if f.selectedStatus != nil {
		return f.selectedStatus, f.selectErr
	}
	return &imap.MailboxStatus{UidValidity: 830}, f.selectErr
}

func (f *fakeMoveCommandClient) UidSearch(criteria *imap.SearchCriteria) ([]uint32, error) {
	f.criteria = criteria
	if f.sourceUIDMissing {
		return nil, f.searchErr
	}
	return []uint32{42}, f.searchErr
}

func (f *fakeMoveCommandClient) Support(capability string) (bool, error) {
	f.supportCalls = append(f.supportCalls, capability)
	return f.moveSupported, f.supportErr
}

func (f *fakeMoveCommandClient) Execute(command imap.Commander, _ responses.Handler) (*imap.StatusResp, error) {
	f.command = command
	return f.status, f.executeErr
}

func TestUIDExistsUsesExactReadOnlyUIDSearch(t *testing.T) {
	client := &fakeUIDSearchClient{uids: []uint32{8, 42}, uidValidity: 912}
	exists, err := uidExists(context.Background(), client, " Inbox ", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("UIDExists returned false for returned UID 42")
	}
	if client.selected != "Inbox" || !client.readOnly {
		t.Fatalf("selected mailbox = %q readOnly=%t, want Inbox read-only", client.selected, client.readOnly)
	}
	if client.criteria == nil || client.criteria.Uid == nil || client.criteria.Uid.String() != "42" {
		t.Fatalf("search criteria = %+v, want exact UID 42", client.criteria)
	}
	if len(client.criteria.WithFlags) != 0 || len(client.criteria.WithoutFlags) != 0 || !client.criteria.Since.IsZero() {
		t.Fatalf("search criteria is not bounded to UID: %+v", client.criteria)
	}
}

func TestUIDExistsWithValidityReturnsSelectedMailboxGeneration(t *testing.T) {
	client := &fakeUIDSearchClient{uids: []uint32{42}, uidValidity: 912}
	exists, uidValidity, err := uidExistsWithValidity(context.Background(), client, "Inbox", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || uidValidity != 912 {
		t.Fatalf("UID existence result = exists %t UIDVALIDITY %d, want true/912", exists, uidValidity)
	}
}

func TestExistingUIDsWithValidityUsesOneExactBatchSearch(t *testing.T) {
	client := &fakeUIDSearchClient{uids: []uint32{8, 42, 99}, uidValidity: 912}
	existing, uidValidity, err := existingUIDsWithValidity(context.Background(), client, "Inbox", []uint32{42, 8, 42})
	if err != nil {
		t.Fatal(err)
	}
	if uidValidity != 912 || !reflect.DeepEqual(existing, []uint32{8, 42}) {
		t.Fatalf("batch UID result = %v generation %d, want [8 42]/912", existing, uidValidity)
	}
	if client.criteria == nil || client.criteria.Uid == nil || client.criteria.Uid.String() != "8,42" {
		t.Fatalf("batch UID criteria = %+v, want exact set 8,42", client.criteria)
	}
}

func TestUIDExistsBoundsIMAPTimeoutToContextDeadline(t *testing.T) {
	fetcher := &Fetcher{Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bounded, err := fetcher.boundedByContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bounded == fetcher {
		t.Fatal("bounded fetcher mutated the shared fetcher")
	}
	if bounded.Timeout <= 0 || bounded.Timeout > 2*time.Second {
		t.Fatalf("bounded timeout = %s, want within the two-second context deadline", bounded.Timeout)
	}
	if fetcher.Timeout != 30*time.Second {
		t.Fatalf("shared fetcher timeout = %s, want 30s", fetcher.Timeout)
	}

	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	if _, err := fetcher.boundedByContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("boundedByContext canceled error = %v, want context canceled", err)
	}
}

func TestUIDExistsReturnsFalseWhenExactUIDIsAbsent(t *testing.T) {
	client := &fakeUIDSearchClient{uids: []uint32{41, 43}, uidValidity: 912}
	exists, err := uidExists(context.Background(), client, "Inbox", 42)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("UIDExists accepted a different returned UID")
	}
}

func TestMoveAndUIDExistenceValidateBeforeProtocolCommands(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx     context.Context
		mailbox string
		uid     uint32
	}{
		"canceled": {ctx: canceled, mailbox: "Inbox", uid: 42},
		"mailbox":  {ctx: context.Background(), uid: 42},
		"uid":      {ctx: context.Background(), mailbox: "Inbox"},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeUIDSearchClient{}
			if _, err := uidExists(tc.ctx, client, tc.mailbox, tc.uid); err == nil {
				t.Fatal("UIDExists accepted invalid input")
			}
			if client.selected != "" || client.criteria != nil {
				t.Fatal("UIDExists used protocol commands after validation failure")
			}
		})
	}

	client := &fakeMoveCommandClient{moveSupported: true}
	if _, err := moveMessageWithReceipt(context.Background(), client, "", "Inbox", 42, 830); err == nil {
		t.Fatal("move accepted an empty source mailbox")
	}
	if client.selected != "" || client.command != nil {
		t.Fatal("move used protocol commands after validation failure")
	}
}

type fakeUIDSearchClient struct {
	uids        []uint32
	uidValidity uint32
	err         error
	selected    string
	readOnly    bool
	criteria    *imap.SearchCriteria
}

func (f *fakeUIDSearchClient) Select(mailbox string, readOnly bool) (*imap.MailboxStatus, error) {
	f.selected = mailbox
	f.readOnly = readOnly
	return &imap.MailboxStatus{UidValidity: f.uidValidity}, nil
}

func (f *fakeUIDSearchClient) UidSearch(criteria *imap.SearchCriteria) ([]uint32, error) {
	f.criteria = criteria
	return append([]uint32(nil), f.uids...), f.err
}

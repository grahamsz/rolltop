package imapclient

import (
	"bytes"
	"testing"

	"github.com/emersion/go-imap"

	"rolltop/backend/syncer"
)

func TestParseAppendReceiptAcceptsTaggedAPPENDUID(t *testing.T) {
	resp, err := imap.ReadResp(imap.NewReader(bytes.NewBufferString("A004 OK [APPENDUID 830 109] APPEND completed\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	status, ok := resp.(*imap.StatusResp)
	if !ok {
		t.Fatalf("response type = %T, want *imap.StatusResp", resp)
	}
	receipt := parseAppendReceipt(status)
	if receipt == nil || receipt.UIDValidity != 830 || receipt.UID != 109 {
		t.Fatalf("receipt = %+v, want UIDVALIDITY 830 UID 109", receipt)
	}
}

func TestParseAppendReceiptRejectsMalformedAPPENDUID(t *testing.T) {
	for name, status := range map[string]*imap.StatusResp{
		"nil status":          nil,
		"absent":              {Type: imap.StatusRespOk},
		"wrong code":          {Code: "COPYUID", Arguments: []interface{}{"830", "109"}},
		"missing uid":         {Code: appendUIDResponseCode, Arguments: []interface{}{"830"}},
		"extra argument":      {Code: appendUIDResponseCode, Arguments: []interface{}{"830", "109", "extra"}},
		"zero uidvalidity":    {Code: appendUIDResponseCode, Arguments: []interface{}{"0", "109"}},
		"invalid uidvalidity": {Code: appendUIDResponseCode, Arguments: []interface{}{"no", "109"}},
		"overflow validity":   {Code: appendUIDResponseCode, Arguments: []interface{}{"4294967296", "109"}},
		"zero uid":            {Code: appendUIDResponseCode, Arguments: []interface{}{"830", "0"}},
		"uid range":           {Code: appendUIDResponseCode, Arguments: []interface{}{"830", "109:110"}},
		"uid list":            {Code: appendUIDResponseCode, Arguments: []interface{}{"830", "108,109"}},
		"dynamic uid":         {Code: appendUIDResponseCode, Arguments: []interface{}{"830", "*"}},
		"non-string argument": {Code: appendUIDResponseCode, Arguments: []interface{}{uint32(830), "109"}},
	} {
		t.Run(name, func(t *testing.T) {
			if receipt := parseAppendReceipt(status); receipt != nil {
				t.Fatalf("receipt = %+v, want nil", receipt)
			}
		})
	}
}

func TestMatchAppendedMessageDisambiguatesConcurrentSameMessageID(t *testing.T) {
	appended := []byte("Message-ID: <same@example.test>\r\nSubject: wanted\r\n\r\nwanted\r\n")
	candidates := []syncer.FetchedMessage{
		{UID: 109, Raw: []byte("Message-ID: <same@example.test>\r\nSubject: other\r\n\r\nother\r\n")},
		{UID: 108, Raw: []byte("Message-ID: <same@example.test>\nSubject: wanted\n\nwanted\n")},
	}

	matched, ok := matchAppendedMessage(appended, candidates)
	if !ok || matched.UID != 108 {
		t.Fatalf("match = UID %d ok=%t, want canonical-content UID 108", matched.UID, ok)
	}
	if matched.AppendUIDAuthoritative {
		t.Fatal("content-correlated fallback was marked authoritative")
	}
}

func TestMatchAppendedMessageRejectsWrongUIDNextCandidate(t *testing.T) {
	appended := []byte("Message-ID: <wanted@example.test>\r\n\r\nwanted\r\n")
	wrong := syncer.FetchedMessage{
		UID: 110,
		Raw: []byte("Message-ID: <wanted@example.test>\r\n\r\nconcurrent\r\n"),
	}
	if appendRawMatches(appended, wrong.Raw) {
		t.Fatal("wrong UIDNEXT candidate matched appended content")
	}
	if matched, ok := matchAppendedMessage(appended, []syncer.FetchedMessage{wrong}); ok {
		t.Fatalf("wrong candidate matched as UID %d", matched.UID)
	}
}

func TestAppendCandidateUIDsExcludePreAppendMessageIDMatches(t *testing.T) {
	got := appendCandidateUIDs([]uint32{4, 9, 13, 12, 10}, 10, 13)
	want := []uint32{12, 10}
	if len(got) != len(want) {
		t.Fatalf("candidate UIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate UIDs = %v, want %v", got, want)
		}
	}
}

func TestAppendCandidateUIDsRequirePreAppendBoundary(t *testing.T) {
	if got := appendCandidateUIDs([]uint32{4, 9, 12}, 0, 13); len(got) != 0 {
		t.Fatalf("candidate UIDs without pre-append UIDNEXT = %v, want none", got)
	}
	if got := appendCandidateUIDs([]uint32{10, 11, 12}, 10, 0); len(got) != 0 {
		t.Fatalf("candidate UIDs without post-append UIDNEXT = %v, want none", got)
	}
}

func TestAppendUIDNextFallbackRequiresPreAppendBoundary(t *testing.T) {
	if uid, ok := appendUIDNextCandidate(0, 13); ok || uid != 0 {
		t.Fatalf("UIDNEXT fallback without pre-append boundary = UID %d ok=%t, want unavailable", uid, ok)
	}
	if uid, ok := appendUIDNextCandidate(10, 13); !ok || uid != 12 {
		t.Fatalf("UIDNEXT fallback with bounds = UID %d ok=%t, want UID 12", uid, ok)
	}
}

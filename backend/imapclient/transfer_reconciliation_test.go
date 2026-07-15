package imapclient

import (
	"strings"
	"testing"
)

func TestTransferReconciliationEmptyMessageIDUsesBoundedPostSnapshotRange(t *testing.T) {
	criteria, search, err := transferReconciliationSearchCriteria("", 41, 46)
	if err != nil || !search {
		t.Fatalf("criteria search=%t err=%v", search, err)
	}
	if criteria == nil || criteria.Uid == nil || criteria.Uid.String() != "41:45" || len(criteria.Header) != 0 {
		t.Fatalf("criteria=%+v UID=%v, want raw-only UID 41:45", criteria, criteria.Uid)
	}
	if err := validatePostSnapshotCandidateUIDs([]uint32{41, 43, 45}, 41, 46); err != nil {
		t.Fatalf("valid post-snapshot candidates: %v", err)
	}
	if err := validatePostSnapshotCandidateUIDs([]uint32{40}, 41, 46); err == nil {
		t.Fatal("accepted a pre-snapshot candidate")
	}
	if err := validatePostSnapshotCandidateUIDs([]uint32{46}, 41, 46); err == nil {
		t.Fatal("accepted a candidate at current UIDNEXT")
	}
	if _, _, err := transferReconciliationSearchCriteria("", 46, 45); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("reversed range error=%v, want ambiguity", err)
	}
}

func TestTransferReconciliationMessageIDSearchPathRemainsUnbounded(t *testing.T) {
	criteria, search, err := transferReconciliationSearchCriteria(" <copy@example.test> ", 41, 46)
	if err != nil || !search {
		t.Fatalf("criteria search=%t err=%v", search, err)
	}
	if criteria.Uid != nil || criteria.Header.Get("Message-ID") != "<copy@example.test>" {
		t.Fatalf("Message-ID criteria UID=%v header=%q", criteria.Uid, criteria.Header.Get("Message-ID"))
	}
}

func TestTransferReconciliationCandidateBound(t *testing.T) {
	atLimit := make([]uint32, maxTransferReconciliationCandidates)
	for i := range atLimit {
		atLimit[i] = uint32(i + 1)
	}
	got, err := boundedTransferReconciliationCandidates(atLimit)
	if err != nil || len(got) != maxTransferReconciliationCandidates {
		t.Fatalf("at-limit candidates=%d err=%v", len(got), err)
	}
	overLimit := append(append([]uint32(nil), atLimit...), uint32(len(atLimit)+1))
	if _, err := boundedTransferReconciliationCandidates(overLimit); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("over-limit error=%v, want conservative ambiguity", err)
	}
}

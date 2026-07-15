package syncer

import (
	"errors"
	"testing"
)

func TestMoveOutcomeUnknownWrapsAndClassifiesCause(t *testing.T) {
	cause := errors.New("connection reset")
	err := MoveOutcomeUnknown(cause)
	if !IsMoveOutcomeUnknown(err) || !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want classified error preserving cause", err)
	}
	if again := MoveOutcomeUnknown(err); again != err {
		t.Fatal("MoveOutcomeUnknown wrapped an already classified error twice")
	}
	if MoveOutcomeUnknown(nil) != nil || IsMoveOutcomeUnknown(cause) {
		t.Fatal("nil or definitive failure was classified as outcome unknown")
	}
}

func TestAppendAppliedWrapsAndClassifiesCause(t *testing.T) {
	cause := errors.New("UID lookup failed")
	err := AppendApplied(cause)
	if !IsAppendApplied(err) || !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want applied classification preserving cause", err)
	}
	if again := AppendApplied(err); again != err {
		t.Fatal("AppendApplied wrapped an already classified error twice")
	}
	if AppendApplied(nil) != nil || IsAppendApplied(cause) {
		t.Fatal("nil or pre-APPEND failure was classified as applied")
	}
}

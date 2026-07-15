package syncer

import "testing"

func TestMailboxSpecialUseRoleRecognizesLocalizedAllMail(t *testing.T) {
	if got := mailboxSpecialUseRole([]string{"\\HasNoChildren", "\\All"}); got != "all" {
		t.Fatalf("special-use role = %q, want all", got)
	}
	if got := mailboxSpecialUseRole([]string{"\\All", "\\Junk"}); got != "junk" {
		t.Fatalf("junk precedence role = %q, want junk", got)
	}
}

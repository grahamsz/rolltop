package main

import (
	"fmt"
	"testing"
	"time"

	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

func TestBootstrapHamCandidatesRequireEstablishedMostlyReadSender(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	messages := []bootstrapEnvelope{
		{UID: 1, From: "Wanted <wanted@example.test>", InternalDate: now.Add(-40 * 24 * time.Hour), Flags: []string{`\Seen`}},
		{UID: 2, From: "Wanted <wanted@example.test>", InternalDate: now.Add(-25 * 24 * time.Hour), Flags: []string{`\Seen`}},
		{UID: 3, From: "Wanted <wanted@example.test>", InternalDate: now.Add(-10 * 24 * time.Hour), Flags: []string{`\Seen`}},
		{UID: 4, From: "Wanted <wanted@example.test>", InternalDate: now.Add(-5 * 24 * time.Hour), Flags: []string{`\Seen`}},
		{UID: 5, From: "Wanted <wanted@example.test>", InternalDate: now.Add(-3 * 24 * time.Hour)},
		{UID: 6, From: "One off <one@example.test>", InternalDate: now.Add(-10 * 24 * time.Hour), Flags: []string{`\Seen`}},
		{UID: 7, From: "Wanted <wanted@example.test>", InternalDate: now.Add(-time.Hour), Flags: []string{`\Seen`}},
	}
	candidates := bootstrapHamMetadataCandidates(messages, now)
	if len(candidates) != 4 {
		t.Fatalf("ham metadata candidates = %d, want four established and old-enough reads: %+v", len(candidates), candidates)
	}
	for _, candidate := range candidates {
		if bootstrapSenderAddress(candidate.From) != "wanted@example.test" || candidate.UID == 7 {
			t.Fatalf("unexpected ham candidate: %+v", candidate)
		}
	}
	if candidates[0].UID != 4 || candidates[len(candidates)-1].UID != 1 {
		t.Fatalf("candidate order = %v..%v, want newest-first", candidates[0].UID, candidates[len(candidates)-1].UID)
	}
}

func TestBootstrapHamCandidateSenderCap(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	messages := make([]bootstrapEnvelope, 0, 30)
	for index := 0; index < 30; index++ {
		messages = append(messages, bootstrapEnvelope{
			UID: uint32(index + 1), From: "Bulk <bulk@example.test>",
			InternalDate: now.Add(-time.Duration(index+3) * 24 * time.Hour), Flags: []string{`\Seen`},
		})
	}
	if got := len(bootstrapHamMetadataCandidates(messages, now)); got != bootstrapMaximumPerSender {
		t.Fatalf("per-sender selected = %d, want %d", got, bootstrapMaximumPerSender)
	}
}

func TestBootstrapAutomaticHamUsesIndependentRuleSafeguards(t *testing.T) {
	classifier, err := spammodel.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	ordinary := spammodel.Message{
		Subject: "Notes from Tuesday's engineering meeting",
		Body:    "Hi team, attached are the action items. Please review the database migration before Friday's meeting. Best regards, Alex",
		From:    "alex@example.test", To: []string{"team@example.test"}, MIMEType: "text/plain",
	}
	if !bootstrapAutomaticHamEligible(classifier, ordinary) {
		t.Fatal("ordinary low-risk mail was rejected as automatic ham")
	}
	newsletter := spammodel.Message{
		Subject: "AsWeMove member update",
		Body:    "AsWeMove Thank you for being a member. Your order is being prepared and no action is required. View your receipt at https://account.example.org/orders/123 or manage preferences at https://account.example.org/preferences.",
		From:    "care@account.example.org", To: []string{"member@example.org"}, MIMEType: "text/html", HTML: true,
	}
	score, err := classifier.Classify(newsletter)
	if err != nil {
		t.Fatal(err)
	}
	if score.Probability >= bootstrapMaximumHamProbability || !bootstrapAutomaticHamEligible(classifier, newsletter) {
		t.Fatalf("wanted newsletter should remain eligible below cutoff %.3f: %+v", bootstrapMaximumHamProbability, score)
	}
	spam := spammodel.Message{
		Subject: "URGENT: claim your FREE cash prize now",
		Body:    fmt.Sprintf("Congratulations winner! Click here to claim your prize. %s", "Act now; limited time offer expires today."),
		From:    "offers@bulk-mail.invalid", To: []string{"reader@example.test"}, MIMEType: "text/html", HTML: true,
	}
	if bootstrapAutomaticHamEligible(classifier, spam) {
		t.Fatal("obvious spam was accepted as automatic ham")
	}
}

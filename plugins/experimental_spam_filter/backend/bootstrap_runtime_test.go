package main

import (
	"context"
	"testing"
	"time"

	"rolltop/backend/syncer"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

func TestExactBootstrapCandidatesUseInternalDateAndExactBounds(t *testing.T) {
	cutoff := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	before := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	candidates := []syncer.TrainingCandidateMetadata{
		{UID: 1, InternalDate: cutoff},
		{UID: 2, InternalDate: before.Add(-time.Nanosecond)},
		{UID: 3, InternalDate: before},
		{UID: 4, InternalDate: cutoff.Add(-time.Nanosecond)},
		{UID: 5, Date: cutoff.Add(time.Hour)},
		{UID: 6, InternalDate: before.Add(time.Hour), Date: cutoff.Add(time.Hour)},
		{UID: 7},
	}
	filtered := exactBootstrapCandidates(candidates, cutoff, before)
	if len(filtered) != 3 || filtered[0].UID != 1 || filtered[1].UID != 2 || filtered[2].UID != 5 {
		t.Fatalf("exact candidates = %+v, want UIDs 1, 2, 5", filtered)
	}
	query := bootstrapTrainingCandidateQuery(cutoff, before, true)
	if !query.Since.Equal(cutoff) || !query.Before.Equal(before.AddDate(0, 0, 1)) || !query.SeenOnly || query.Limit != syncer.MaxTrainingCandidateCount {
		t.Fatalf("day-widened exact-filter query = %+v", query)
	}
}

func TestExplicitBootstrapFingerprintFilteringIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "bootstrap-filter-first@example.test", "First", 1)
	second := createSpamTestMessage(t, ctx, st, "bootstrap-filter-second@example.test", "Second", 2)
	firstExplicit := spammodel.Message{Subject: "first explicit", Body: "first explicit payload"}
	secondExplicit := spammodel.Message{Subject: "second explicit", Body: "second explicit payload"}
	remaining := spammodel.Message{Subject: "remaining", Body: "remaining automatic payload"}
	firstFingerprint := PersonalBayesFingerprint(firstExplicit)
	secondFingerprint := PersonalBayesFingerprint(secondExplicit)
	remainingFingerprint := PersonalBayesFingerprint(remaining)
	if _, err := LearnPersonalBayesFingerprint(ctx, st.DB(), first.UserID, PersonalBayesSourceExplicit, feedbackHam, firstFingerprint, firstExplicit); err != nil {
		t.Fatal(err)
	}
	if _, err := LearnPersonalBayesFingerprint(ctx, st.DB(), second.UserID, PersonalBayesSourceExplicit, feedbackSpam, secondFingerprint, secondExplicit); err != nil {
		t.Fatal(err)
	}
	explicit, err := explicitPersonalBayesFingerprints(ctx, st.DB(), first.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := explicit[firstFingerprint]; !found {
		t.Fatal("first user's explicit fingerprint was omitted")
	}
	if _, leaked := explicit[secondFingerprint]; leaked {
		t.Fatal("second user's explicit fingerprint leaked into first user's filter")
	}
	documents := map[[32]byte]PersonalBayesTrainingDocument{
		firstFingerprint:     {Fingerprint: firstFingerprint, Label: feedbackSpam, Message: firstExplicit},
		secondFingerprint:    {Fingerprint: secondFingerprint, Label: feedbackHam, Message: secondExplicit},
		remainingFingerprint: {Fingerprint: remainingFingerprint, Label: feedbackSpam, Message: remaining},
	}
	removedSpam, removedHam := removeExplicitBootstrapDocuments(documents, explicit)
	if removedSpam != 1 || removedHam != 0 || len(documents) != 2 {
		t.Fatalf("explicit filtering removed spam=%d ham=%d documents=%d", removedSpam, removedHam, len(documents))
	}
	if _, found := documents[firstFingerprint]; found {
		t.Fatal("explicitly covered document remained in automatic snapshot")
	}
	if _, found := documents[secondFingerprint]; !found {
		t.Fatal("other tenant's explicit label removed this tenant's candidate")
	}
}

func TestBootstrapSelectedCandidateCountsReplacePreviewAndStayTenantScoped(t *testing.T) {
	ctx := context.Background()
	st := newSpamTestStore(t)
	first := createSpamTestMessage(t, ctx, st, "candidate-count-first@example.test", "First", 1)
	second := createSpamTestMessage(t, ctx, st, "candidate-count-second@example.test", "Second", 2)
	cutoff := time.Now().UTC().AddDate(0, -6, 0)
	if err := startBootstrapRecord(ctx, st.DB(), first.UserID, cutoff, 500, 2000); err != nil {
		t.Fatal(err)
	}
	if err := startBootstrapRecord(ctx, st.DB(), second.UserID, cutoff, 400, 1200); err != nil {
		t.Fatal(err)
	}
	if err := updateBootstrapCandidateCounts(ctx, st.DB(), first.UserID, 37, 83); err != nil {
		t.Fatal(err)
	}
	firstRecord, err := getBootstrap(ctx, st.DB(), first.UserID)
	if err != nil {
		t.Fatal(err)
	}
	secondRecord, err := getBootstrap(ctx, st.DB(), second.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if firstRecord.CandidateSpam != 37 || firstRecord.CandidateHam != 83 {
		t.Fatalf("selected first candidate counts = %d/%d, want 37/83", firstRecord.CandidateSpam, firstRecord.CandidateHam)
	}
	if secondRecord.CandidateSpam != 400 || secondRecord.CandidateHam != 1200 {
		t.Fatalf("second tenant candidate counts changed to %d/%d", secondRecord.CandidateSpam, secondRecord.CandidateHam)
	}
}

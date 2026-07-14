package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"rolltop/backend/imapclient"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type preparedBootstrapSource struct {
	Account store.MailAccount
	Inbox   store.Mailbox
	Junk    store.Mailbox
}

type remoteBootstrapCandidate struct {
	Source   int
	Envelope bootstrapEnvelope
}

func (p *spamFilterPlugin) apiPreviewBootstrap(host plugins.APIHost, st *store.Store, _ *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	selections, ok := decodeBootstrapSelections(host, w, r)
	if !ok {
		return
	}
	sources, err := prepareBootstrapSources(r.Context(), st, userID, selections)
	if err != nil {
		writeBootstrapSelectionError(host, w, err)
		return
	}
	preview, err := searchBootstrapPreview(r.Context(), host.MasterKey(), sources, time.Now().UTC())
	if err != nil {
		host.WriteAPIError(w, http.StatusBadGateway, "could not read the selected IMAP folders")
		return
	}
	host.WriteJSON(w, preview)
}

func (p *spamFilterPlugin) apiStartBootstrap(host plugins.APIHost, st *store.Store, _ *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	selections, ok := decodeBootstrapSelections(host, w, r)
	if !ok {
		return
	}
	sources, err := prepareBootstrapSources(r.Context(), st, userID, selections)
	if err != nil {
		writeBootstrapSelectionError(host, w, err)
		return
	}
	if !p.reserveBootstrap(userID) {
		host.WriteAPIError(w, http.StatusConflict, "a personal spam-training bootstrap is already running")
		return
	}
	preview, err := searchBootstrapPreview(r.Context(), host.MasterKey(), sources, time.Now().UTC())
	if err != nil {
		p.releaseBootstrap(userID)
		host.WriteAPIError(w, http.StatusBadGateway, "could not read the selected IMAP folders")
		return
	}
	_, userDB, err := pluginUserDB(r.Context(), host, userID)
	if err != nil {
		p.releaseBootstrap(userID)
		host.ServerError(w, err)
		return
	}
	cutoff := time.Unix(preview.CutoffAt, 0).UTC()
	if err := startBootstrapRecord(r.Context(), userDB, userID, cutoff,
		min(preview.Spam, bootstrapMaximumPerClass), min(preview.Ham, bootstrapMaximumHamExamined)); err != nil {
		p.releaseBootstrap(userID)
		host.ServerError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !p.setBootstrapCancel(userID, cancel) {
		cancel()
		p.releaseBootstrap(userID)
		host.WriteAPIError(w, http.StatusConflict, "bootstrap could not be started")
		return
	}
	go p.runBootstrap(ctx, host, userID, sources, preview)
	host.WriteJSON(w, map[string]any{"ok": true, "status": "running", "preview": preview})
}

func (p *spamFilterPlugin) apiCancelBootstrap(host plugins.APIHost, _ *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	if !p.cancelBootstrap(userID) {
		host.WriteAPIError(w, http.StatusConflict, "no personal spam-training bootstrap is running")
		return
	}
	host.WriteJSON(w, map[string]any{"ok": true, "status": "cancelling"})
}

func (p *spamFilterPlugin) apiResetBootstrap(host plugins.APIHost, _ *sql.DB, userID int64, w http.ResponseWriter, r *http.Request) {
	if !host.VerifyCSRF(w, r) {
		return
	}
	if p.bootstrapActive(userID) {
		host.WriteAPIError(w, http.StatusConflict, "cancel the running bootstrap before resetting inferred training")
		return
	}
	_, userDB, err := pluginUserDB(r.Context(), host, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	result, err := ResetAutomaticPersonalBayes(r.Context(), userDB, userID)
	if err != nil {
		host.ServerError(w, err)
		return
	}
	if err := resetBootstrapRecord(r.Context(), userDB, userID); err != nil {
		host.ServerError(w, err)
		return
	}
	notifyUserChanged(host, userID)
	host.WriteJSON(w, map[string]any{"ok": true, "counts": result.Counts})
}

func decodeBootstrapSelections(host plugins.APIHost, w http.ResponseWriter, r *http.Request) ([]bootstrapMailboxSelection, bool) {
	var input struct {
		Selections []bootstrapMailboxSelection `json:"selections"`
	}
	if !host.DecodeJSON(w, r, &input) {
		return nil, false
	}
	if len(input.Selections) == 0 || len(input.Selections) > 32 {
		host.WriteAPIError(w, http.StatusBadRequest, "select an Inbox and Spam/Junk folder")
		return nil, false
	}
	return input.Selections, true
}

func prepareBootstrapSources(ctx context.Context, st *store.Store, userID int64, selections []bootstrapMailboxSelection) ([]preparedBootstrapSource, error) {
	seenAccounts := make(map[int64]bool, len(selections))
	sources := make([]preparedBootstrapSource, 0, len(selections))
	for _, selection := range selections {
		if selection.AccountID <= 0 || selection.InboxMailboxID <= 0 || selection.JunkMailboxID <= 0 ||
			selection.InboxMailboxID == selection.JunkMailboxID || seenAccounts[selection.AccountID] {
			return nil, store.ErrNotFound
		}
		account, err := st.GetMailAccountForUser(ctx, userID, selection.AccountID)
		if err != nil {
			return nil, err
		}
		inbox, err := st.GetMailboxForUser(ctx, userID, selection.InboxMailboxID)
		if err != nil {
			return nil, err
		}
		junk, err := st.GetMailboxForUser(ctx, userID, selection.JunkMailboxID)
		if err != nil {
			return nil, err
		}
		if account.UserID != userID || inbox.UserID != userID || junk.UserID != userID ||
			inbox.AccountID != account.ID || junk.AccountID != account.ID {
			return nil, store.ErrNotFound
		}
		seenAccounts[account.ID] = true
		sources = append(sources, preparedBootstrapSource{Account: account, Inbox: inbox, Junk: junk})
	}
	return sources, nil
}

func writeBootstrapSelectionError(host plugins.APIHost, w http.ResponseWriter, err error) {
	if store.IsNotFound(err) {
		host.WriteAPIError(w, http.StatusNotFound, "selected mail folders were not found")
		return
	}
	host.ServerError(w, err)
}

func searchBootstrapPreview(ctx context.Context, masterKey []byte, sources []preparedBootstrapSource, now time.Time) (bootstrapPreview, error) {
	cutoff := now.AddDate(0, -6, 0)
	before := now.Add(-bootstrapMinimumAge)
	fetcher := &imapclient.Fetcher{MasterKey: masterKey, Timeout: 2 * time.Minute, BatchSize: 25}
	preview := bootstrapPreview{CutoffAt: cutoff.Unix(), Accounts: make([]bootstrapPreviewMailbox, 0, len(sources))}
	for _, source := range sources {
		spam, err := fetcher.SearchTrainingCandidates(ctx, source.Account, source.Junk.Name,
			bootstrapTrainingCandidateQuery(cutoff, before, false))
		if err != nil {
			return bootstrapPreview{}, err
		}
		ham, err := fetcher.SearchTrainingCandidates(ctx, source.Account, source.Inbox.Name,
			bootstrapTrainingCandidateQuery(cutoff, before, true))
		if err != nil {
			return bootstrapPreview{}, err
		}
		spamCandidates := exactBootstrapCandidates(spam.Candidates, cutoff, before)
		hamCandidates := exactBootstrapCandidates(ham.Candidates, cutoff, before)
		label := strings.TrimSpace(source.Account.Label)
		if label == "" {
			label = source.Account.Email
		}
		preview.Accounts = append(preview.Accounts, bootstrapPreviewMailbox{
			AccountID: source.Account.ID, AccountLabel: label,
			InboxMailboxID: source.Inbox.ID, InboxName: source.Inbox.Name,
			JunkMailboxID: source.Junk.ID, JunkName: source.Junk.Name,
			SpamCandidates: len(spamCandidates), HamCandidates: len(hamCandidates),
		})
		preview.Spam += len(spamCandidates)
		preview.Ham += len(hamCandidates)
	}
	return preview, nil
}

func (p *spamFilterPlugin) runBootstrap(ctx context.Context, host plugins.BackendHost, userID int64, sources []preparedBootstrapSource, preview bootstrapPreview) {
	defer p.releaseBootstrap(userID)
	_, db, err := pluginUserDB(ctx, host, userID)
	if err != nil {
		return
	}
	fail := func(status, message string) {
		_ = finishBootstrapRecord(context.Background(), db, userID, status, message)
	}
	if err := ctx.Err(); err != nil {
		fail("cancelled", "bootstrap cancelled")
		return
	}
	classifier, _, _ := p.model()
	if classifier == nil {
		fail("failed", "the checked-in named-rule model is unavailable")
		return
	}
	cutoff := time.Unix(preview.CutoffAt, 0).UTC()
	now := time.Now().UTC()
	before := now.Add(-bootstrapMinimumAge)
	fetcher := &imapclient.Fetcher{MasterKey: host.MasterKey(), Timeout: 2 * time.Minute, BatchSize: 25}
	spamCandidates := make([]remoteBootstrapCandidate, 0, bootstrapMaximumPerClass*len(sources))
	inboxCandidates := make([]remoteBootstrapCandidate, 0, syncer.MaxTrainingCandidateCount*len(sources))
	for index, source := range sources {
		if err := updateBootstrapRecord(ctx, db, userID, 0, 0, 0, 0, source.Junk.Name, ""); err != nil {
			fail("failed", "could not save bootstrap progress")
			return
		}
		junkSearch, err := fetcher.SearchTrainingCandidates(ctx, source.Account, source.Junk.Name,
			bootstrapTrainingCandidateQuery(cutoff, before, false))
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				fail("cancelled", "bootstrap cancelled")
			} else {
				fail("failed", "could not search the selected Spam/Junk folder")
			}
			return
		}
		for _, candidate := range exactBootstrapCandidates(junkSearch.Candidates, cutoff, before) {
			spamCandidates = append(spamCandidates, remoteBootstrapCandidate{Source: index, Envelope: bootstrapEnvelopeFromMetadata(source.Account.ID, source.Junk, candidate)})
		}
		if err := updateBootstrapRecord(ctx, db, userID, 0, 0, 0, 0, source.Inbox.Name, ""); err != nil {
			fail("failed", "could not save bootstrap progress")
			return
		}
		inboxSearch, err := fetcher.SearchTrainingCandidates(ctx, source.Account, source.Inbox.Name,
			bootstrapTrainingCandidateQuery(cutoff, before, false))
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				fail("cancelled", "bootstrap cancelled")
			} else {
				fail("failed", "could not search the selected Inbox")
			}
			return
		}
		for _, candidate := range exactBootstrapCandidates(inboxSearch.Candidates, cutoff, before) {
			inboxCandidates = append(inboxCandidates, remoteBootstrapCandidate{Source: index, Envelope: bootstrapEnvelopeFromMetadata(source.Account.ID, source.Inbox, candidate)})
		}
	}

	sortRemoteBootstrapCandidates(spamCandidates)
	if len(spamCandidates) > bootstrapMaximumPerClass {
		spamCandidates = spamCandidates[:bootstrapMaximumPerClass]
	}
	hamEnvelopes := make([]bootstrapEnvelope, 0, len(inboxCandidates))
	inboxByKey := make(map[string]remoteBootstrapCandidate, len(inboxCandidates))
	for _, candidate := range inboxCandidates {
		hamEnvelopes = append(hamEnvelopes, candidate.Envelope)
		inboxByKey[bootstrapRemoteKey(candidate.Envelope)] = candidate
	}
	hamSelected := bootstrapHamMetadataCandidates(hamEnvelopes, now)
	hamCandidates := make([]remoteBootstrapCandidate, 0, len(hamSelected))
	for _, envelope := range hamSelected {
		if candidate, ok := inboxByKey[bootstrapRemoteKey(envelope)]; ok {
			hamCandidates = append(hamCandidates, candidate)
		}
	}
	if err := updateBootstrapCandidateCounts(ctx, db, userID, len(spamCandidates), len(hamCandidates)); err != nil {
		fail("failed", "could not save selected bootstrap candidates")
		return
	}

	documents := make(map[[32]byte]PersonalBayesTrainingDocument)
	ambiguous := make(map[[32]byte]bool)
	examined, rejected := 0, 0
	acceptedSpam, acceptedHam := 0, 0
	addDocument := func(label string, parsed bootstrapParsedMessage) {
		fingerprint := PersonalBayesFingerprint(parsed.Model)
		if ambiguous[fingerprint] {
			rejected++
			return
		}
		if existing, ok := documents[fingerprint]; ok {
			if existing.Label != label {
				delete(documents, fingerprint)
				ambiguous[fingerprint] = true
				if existing.Label == feedbackSpam {
					acceptedSpam--
				} else {
					acceptedHam--
				}
				rejected++
			}
			return
		}
		documents[fingerprint] = PersonalBayesTrainingDocument{Fingerprint: fingerprint, Label: label, Message: parsed.Model}
		if label == feedbackSpam {
			acceptedSpam++
		} else {
			acceptedHam++
		}
	}
	progress := func(mailbox string) error {
		return updateBootstrapRecord(ctx, db, userID, examined, acceptedSpam, acceptedHam, rejected, mailbox, "")
	}
	if err := fetchRemoteBootstrapCandidates(ctx, fetcher, sources, spamCandidates, func(remote remoteBootstrapCandidate, fetched syncer.TrainingCandidate) error {
		examined++
		parsed, err := parseBootstrapMessage(fetched.Raw, remote.Envelope)
		if err != nil || parsed.IsEncrypted {
			rejected++
		} else {
			addDocument(feedbackSpam, parsed)
		}
		return progress(remote.Envelope.MailboxName)
	}); err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			fail("cancelled", "bootstrap cancelled")
		} else {
			fail("failed", "could not fetch selected Spam/Junk messages")
		}
		return
	}
	if err := fetchRemoteBootstrapCandidates(ctx, fetcher, sources, hamCandidates, func(remote remoteBootstrapCandidate, fetched syncer.TrainingCandidate) error {
		examined++
		if acceptedHam >= bootstrapMaximumPerClass {
			rejected++
			return progress(remote.Envelope.MailboxName)
		}
		parsed, err := parseBootstrapMessage(fetched.Raw, remote.Envelope)
		if err != nil || parsed.IsEncrypted || !bootstrapAutomaticHamEligible(classifier, parsed.Model) {
			rejected++
		} else {
			addDocument(feedbackHam, parsed)
		}
		return progress(remote.Envelope.MailboxName)
	}); err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			fail("cancelled", "bootstrap cancelled")
		} else {
			fail("failed", "could not fetch selected Inbox messages")
		}
		return
	}

	explicitFingerprints, err := explicitPersonalBayesFingerprints(ctx, db, userID)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			fail("cancelled", "bootstrap cancelled")
		} else {
			fail("failed", "could not inspect explicit personal training")
		}
		return
	}
	removedSpam, removedHam := removeExplicitBootstrapDocuments(documents, explicitFingerprints)
	acceptedSpam -= removedSpam
	acceptedHam -= removedHam
	rejected += removedSpam + removedHam
	if acceptedSpam > acceptedHam {
		remove := acceptedSpam - acceptedHam
		fingerprints := sortedBootstrapFingerprints(documents, feedbackSpam)
		for index := 0; index < remove && index < len(fingerprints); index++ {
			delete(documents, fingerprints[index])
			acceptedSpam--
			rejected++
		}
	}
	prepared := make([]PersonalBayesTrainingDocument, 0, len(documents))
	for _, fingerprint := range sortedBootstrapFingerprints(documents, "") {
		prepared = append(prepared, documents[fingerprint])
	}
	result, err := ReplaceAutomaticPersonalBayesSnapshot(ctx, db, userID, prepared)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			fail("cancelled", "bootstrap cancelled")
		} else {
			fail("failed", "could not replace inferred personal training")
		}
		return
	}
	acceptedSpam = int(result.Counts.Automatic.Spam)
	acceptedHam = int(result.Counts.Automatic.Ham)
	if err := updateBootstrapRecord(context.Background(), db, userID, examined, acceptedSpam, acceptedHam, rejected, "", ""); err != nil {
		fail("failed", "could not save bootstrap results")
		return
	}
	if err := finishBootstrapRecord(context.Background(), db, userID, "complete", ""); err != nil {
		return
	}
	notifyUserChanged(host, userID)
}

func bootstrapTrainingCandidateQuery(cutoff, before time.Time, seenOnly bool) syncer.TrainingCandidateQuery {
	// IMAP date searches operate at day precision. Search through the following
	// calendar day, then enforce the exact instant against fetched metadata.
	return syncer.TrainingCandidateQuery{
		Since: cutoff, Before: before.AddDate(0, 0, 1), SeenOnly: seenOnly,
		Limit: syncer.MaxTrainingCandidateCount,
	}
}

func exactBootstrapCandidates(candidates []syncer.TrainingCandidateMetadata, cutoff, before time.Time) []syncer.TrainingCandidateMetadata {
	filtered := make([]syncer.TrainingCandidateMetadata, 0, len(candidates))
	for _, candidate := range candidates {
		date := candidate.InternalDate
		if date.IsZero() {
			date = candidate.Date
		}
		if date.IsZero() || date.Before(cutoff) || !date.Before(before) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func explicitPersonalBayesFingerprints(ctx context.Context, db *sql.DB, userID int64) (map[[32]byte]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT message_fingerprint
		FROM plugin_experimental_spam_bayes_labels
		WHERE user_id = ? AND source = 'explicit'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fingerprints := make(map[[32]byte]struct{})
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(raw) != 32 {
			return nil, ErrPersonalBayesInconsistent
		}
		var fingerprint [32]byte
		copy(fingerprint[:], raw)
		fingerprints[fingerprint] = struct{}{}
	}
	return fingerprints, rows.Err()
}

func removeExplicitBootstrapDocuments(documents map[[32]byte]PersonalBayesTrainingDocument, explicit map[[32]byte]struct{}) (spam, ham int) {
	for fingerprint := range explicit {
		document, found := documents[fingerprint]
		if !found {
			continue
		}
		delete(documents, fingerprint)
		if document.Label == feedbackSpam {
			spam++
		} else if document.Label == feedbackHam {
			ham++
		}
	}
	return spam, ham
}

func bootstrapEnvelopeFromMetadata(accountID int64, mailbox store.Mailbox, candidate syncer.TrainingCandidateMetadata) bootstrapEnvelope {
	return bootstrapEnvelope{
		AccountID: accountID, MailboxID: mailbox.ID, MailboxName: mailbox.Name,
		UID: candidate.UID, MessageID: candidate.MessageID,
		From: strings.Join(candidate.From, ", "), To: strings.Join(candidate.To, ", "),
		Subject: candidate.Subject, Date: candidate.Date, InternalDate: candidate.InternalDate,
		Size: candidate.Size, Flags: append([]string(nil), candidate.Flags...),
	}
}

func sortRemoteBootstrapCandidates(candidates []remoteBootstrapCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i].Envelope.InternalDate, candidates[j].Envelope.InternalDate
		if left.IsZero() {
			left = candidates[i].Envelope.Date
		}
		if right.IsZero() {
			right = candidates[j].Envelope.Date
		}
		if left.Equal(right) {
			return candidates[i].Envelope.UID > candidates[j].Envelope.UID
		}
		return left.After(right)
	})
}

func bootstrapRemoteKey(envelope bootstrapEnvelope) string {
	return fmt.Sprintf("%d:%d:%d", envelope.AccountID, envelope.MailboxID, envelope.UID)
}

func fetchRemoteBootstrapCandidates(ctx context.Context, fetcher syncer.TrainingCandidateFetcher, sources []preparedBootstrapSource, candidates []remoteBootstrapCandidate, handle func(remoteBootstrapCandidate, syncer.TrainingCandidate) error) error {
	bySource := make(map[int][]remoteBootstrapCandidate)
	for _, candidate := range candidates {
		bySource[candidate.Source] = append(bySource[candidate.Source], candidate)
	}
	indexes := make([]int, 0, len(bySource))
	for index := range bySource {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		if index < 0 || index >= len(sources) {
			return errors.New("bootstrap source is invalid")
		}
		group := bySource[index]
		if len(group) == 0 {
			continue
		}
		byUID := make(map[uint32]remoteBootstrapCandidate, len(group))
		uids := make([]uint32, 0, len(group))
		for _, candidate := range group {
			byUID[candidate.Envelope.UID] = candidate
			uids = append(uids, candidate.Envelope.UID)
		}
		mailbox := group[0].Envelope.MailboxName
		if err := fetcher.FetchTrainingCandidates(ctx, sources[index].Account, mailbox, uids, func(fetched syncer.TrainingCandidate) error {
			candidate, ok := byUID[fetched.UID]
			if !ok {
				return errors.New("IMAP returned an unrequested training candidate")
			}
			return handle(candidate, fetched)
		}); err != nil {
			return err
		}
	}
	return nil
}

func sortedBootstrapFingerprints(documents map[[32]byte]PersonalBayesTrainingDocument, label string) [][32]byte {
	fingerprints := make([][32]byte, 0, len(documents))
	for fingerprint, document := range documents {
		if label == "" || document.Label == label {
			fingerprints = append(fingerprints, fingerprint)
		}
	}
	sort.Slice(fingerprints, func(i, j int) bool {
		return strings.Compare(string(fingerprints[i][:]), string(fingerprints[j][:])) < 0
	})
	return fingerprints
}

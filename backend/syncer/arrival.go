// File overview: Delayed Inbox-arrival classification and bounded external-move probes.

package syncer

import (
	"context"
	"errors"
	"strings"
	"time"

	"rolltop/backend/store"
)

const (
	maxInboxArrivalFinalizeBatch   = 100
	maxInboxArrivalProbeCandidates = 20
	inboxArrivalProbeTimeout       = 5 * time.Second
)

// recordInboxArrival gives local transfers a chance to claim a new Inbox UID
// before it becomes a delivery event. Production runners schedule unmatched
// arrivals; direct Service users finalize synchronously for compatibility.
func (s *Service) recordInboxArrival(ctx context.Context, userID, syncRunID int64, msg store.MessageRecord, item FetchedMessage, progress *store.SyncProgress) error {
	fingerprint := store.ArrivalFingerprint{
		MessageIDHash: store.HashedMessageID(msg.MessageIDHeader),
		InternalDate:  msg.InternalDate,
		Size:          msg.Size,
	}
	if len(item.Raw) > 0 {
		fingerprint = store.MessageArrivalFingerprint(item.Raw, msg.MessageIDHeader, msg.InternalDate, msg.Size)
	}
	decision, err := s.Store.HoldOrClassifyInboxArrival(ctx, userID, syncRunID, msg, fingerprint, time.Now().UTC())
	if err != nil {
		return err
	}
	if decision.Arrival.Classification != store.ArrivalPending {
		if decision.EventCreated && progress != nil {
			progress.NewMessages++
			progress.LatestNewFrom = msg.FromAddr
			progress.LatestNewSubject = msg.Subject
			progress.LatestNewMessageID = msg.ID
		}
		return nil
	}
	if s.ScheduleInboxArrival != nil {
		s.ScheduleInboxArrival(userID, msg.AccountID, decision.Arrival.AvailableAt)
		return nil
	}

	// Services constructed without a Runner are used by maintenance commands and
	// tests. Finalizing at the durable deadline preserves their synchronous API.
	created, _, err := s.FinalizePendingInboxArrivals(ctx, userID, msg.AccountID, decision.Arrival.AvailableAt)
	if err != nil {
		return err
	}
	if created > 0 && progress != nil {
		progress.NewMessages += created
		progress.LatestNewFrom = msg.FromAddr
		progress.LatestNewSubject = msg.Subject
		progress.LatestNewMessageID = msg.ID
	}
	return nil
}

// FinalizePendingInboxArrivals proves any uniquely correlated external move,
// then atomically converts the remaining due arrivals into delivery events.
func (s *Service) FinalizePendingInboxArrivals(ctx context.Context, userID, accountID int64, now time.Time) (int, time.Time, error) {
	if s == nil || s.Store == nil {
		return 0, time.Time{}, errors.New("sync store is not configured")
	}
	due, err := s.Store.ListDueInboxArrivals(ctx, userID, accountID, now, maxInboxArrivalFinalizeBatch)
	if err != nil {
		return 0, time.Time{}, err
	}
	if len(due) > 0 {
		validityChecker, _ := s.Fetcher.(UIDValidityExistenceFetcher)
		batchChecker, _ := s.Fetcher.(BatchUIDValidityExistenceFetcher)
		probeCtx, cancel := context.WithTimeout(ctx, inboxArrivalProbeTimeout)
		deferredMessageIDs, probeErr := s.probeExternalInboxMoves(probeCtx, validityChecker,
			batchChecker, userID, accountID, due)
		cancel()
		if probeErr != nil {
			return 0, time.Time{}, probeErr
		}
		if len(deferredMessageIDs) > 0 {
			if err := s.Store.DeferPendingInboxArrivalProbes(ctx, userID, accountID,
				deferredMessageIDs, now.UTC().Add(inboxArrivalRetryDelay)); err != nil {
				return 0, time.Time{}, err
			}
		}
	}
	created, err := s.Store.FinalizeDueInboxArrivals(ctx, userID, accountID, now)
	if err != nil {
		return 0, time.Time{}, err
	}
	nextDue, err := s.Store.NextPendingInboxArrivalDue(ctx, userID, accountID)
	if err != nil {
		if store.IsNotFound(err) {
			return created, time.Time{}, nil
		}
		return created, time.Time{}, err
	}
	return created, nextDue, nil
}

func (s *Service) probeExternalInboxMoves(ctx context.Context, checker UIDValidityExistenceFetcher, batchChecker BatchUIDValidityExistenceFetcher, userID, accountID int64, arrivals []store.PendingInboxArrival) ([]int64, error) {
	type mailboxProbeState struct {
		mailbox     store.Mailbox
		uidValidity int64
		valid       bool
	}
	type sourceProbe struct {
		planIndex       int
		sourceMessageID int64
		uid             uint32
		exists          bool
		absent          bool
	}
	type arrivalProbePlan struct {
		messageID  int64
		candidates []*sourceProbe
		uncertain  bool
	}
	type sourceProbeGroup struct {
		mailbox     store.Mailbox
		uidValidity int64
		sources     []*sourceProbe
	}
	mailboxes := make(map[int64]mailboxProbeState)
	groupsByMailbox := make(map[int64]*sourceProbeGroup)
	var groups []*sourceProbeGroup
	plans := make([]*arrivalProbePlan, 0, len(arrivals))
	for _, arrival := range arrivals {
		plan := &arrivalProbePlan{messageID: arrival.MessageID}
		plans = append(plans, plan)
		if ctx.Err() != nil {
			plan.uncertain = true
			continue
		}
		candidates, err := s.Store.ListPotentialMoveSources(ctx, userID, arrival.MessageID, maxInboxArrivalProbeCandidates)
		if err != nil {
			plan.uncertain = true
			continue
		}
		if len(candidates) > maxInboxArrivalProbeCandidates {
			plan.uncertain = true
			continue
		}
		for _, candidate := range candidates {
			if candidate.Message.AccountID != accountID || candidate.Message.UID == 0 || candidate.SourceUIDValidity <= 0 {
				plan.uncertain = true
				continue
			}
			state, loaded := mailboxes[candidate.Message.MailboxID]
			if !loaded {
				mailbox, mailboxErr := s.Store.GetMailboxForUser(ctx, userID, candidate.Message.MailboxID)
				state.mailbox = mailbox
				if mailboxErr == nil && mailbox.AccountID == accountID && mailbox.UIDValidity > 0 && !mailboxIsInboxOrAllMail(mailbox) {
					state.uidValidity = mailbox.UIDValidity
					state.valid = true
				}
				mailboxes[candidate.Message.MailboxID] = state
			}
			if !state.valid || state.uidValidity != candidate.SourceUIDValidity {
				plan.uncertain = true
				continue
			}
			group := groupsByMailbox[state.mailbox.ID]
			if group == nil {
				group = &sourceProbeGroup{mailbox: state.mailbox, uidValidity: state.uidValidity}
				groupsByMailbox[state.mailbox.ID] = group
				groups = append(groups, group)
			}
			probe := &sourceProbe{planIndex: len(plans) - 1, sourceMessageID: candidate.Message.ID, uid: candidate.Message.UID}
			group.sources = append(group.sources, probe)
			plan.candidates = append(plan.candidates, probe)
		}
	}

	if len(groups) > 0 && checker == nil && batchChecker == nil {
		for _, plan := range plans {
			if len(plan.candidates) > 0 {
				plan.uncertain = true
			}
		}
	}
	var account store.MailAccount
	if len(groups) > 0 && (checker != nil || batchChecker != nil) {
		loadedAccount, err := s.Store.GetMailAccountForUser(ctx, userID, accountID)
		if err != nil {
			for _, plan := range plans {
				if len(plan.candidates) > 0 {
					plan.uncertain = true
				}
			}
		} else {
			account = loadedAccount
		}
	}
	for _, group := range groups {
		if account.ID == 0 || ctx.Err() != nil {
			for _, source := range group.sources {
				plans[source.planIndex].uncertain = true
			}
			continue
		}
		if batchChecker != nil {
			uids := make([]uint32, 0, len(group.sources))
			for _, source := range group.sources {
				uids = append(uids, source.uid)
			}
			existingUIDs, uidValidity, probeErr := batchChecker.ExistingUIDsWithValidity(ctx, account, group.mailbox.Name, uids)
			if probeErr != nil || int64(uidValidity) != group.uidValidity {
				for _, source := range group.sources {
					plans[source.planIndex].uncertain = true
				}
				continue
			}
			existing := make(map[uint32]struct{}, len(existingUIDs))
			for _, uid := range existingUIDs {
				existing[uid] = struct{}{}
			}
			for _, source := range group.sources {
				if _, stillExists := existing[source.uid]; stillExists {
					source.exists = true
					continue
				}
				source.absent = true
			}
			continue
		}
		if checker == nil {
			continue
		}
		for _, source := range group.sources {
			exists, uidValidity, probeErr := checker.UIDExistsWithValidity(ctx, account, group.mailbox.Name, source.uid)
			if probeErr != nil || int64(uidValidity) != group.uidValidity {
				plans[source.planIndex].uncertain = true
				continue
			}
			if exists {
				source.exists = true
			} else {
				source.absent = true
			}
		}
	}

	deferred := make([]int64, 0)
	for _, plan := range plans {
		if plan.uncertain {
			deferred = append(deferred, plan.messageID)
			continue
		}
		var absent []*sourceProbe
		for _, candidate := range plan.candidates {
			if !candidate.exists && !candidate.absent {
				plan.uncertain = true
				break
			}
			if candidate.absent {
				absent = append(absent, candidate)
			}
		}
		if plan.uncertain || len(absent) > 1 {
			deferred = append(deferred, plan.messageID)
			continue
		}
		if len(absent) == 1 {
			if err := s.Store.RecordExpungedMessageFingerprint(ctx, userID, absent[0].sourceMessageID, ""); err != nil {
				return nil, err
			}
		}
	}
	return deferred, nil
}

func mailboxIsInboxOrAllMail(mailbox store.Mailbox) bool {
	role := strings.ToLower(strings.TrimSpace(mailbox.Role))
	if role == "inbox" || role == "all" {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(mailbox.Name))
	return name == "inbox" || name == "all mail" || name == "[gmail]/all mail"
}

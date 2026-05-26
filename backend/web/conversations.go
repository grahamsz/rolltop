// File overview: Conversation grouping, summary construction, search seed handling, and sender identity helpers.

package web

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

type conversationSeed struct {
	Message     store.MessageRecord
	MatchTerms  []string
	MatchFields []string
}

func (s *Server) conversationViews(ctx context.Context, userID int64, seeds []store.MessageRecord, own map[string]bool) ([]conversationView, error) {
	return s.conversationViewsFromSeeds(ctx, userID, conversationSeedsFromMessages(seeds), own, "")
}

func (s *Server) conversationViewsWithSearchSnippet(ctx context.Context, userID int64, seeds []store.MessageRecord, own map[string]bool, query string) ([]conversationView, error) {
	return s.conversationViewsFromSeeds(ctx, userID, conversationSeedsFromMessages(seeds), own, query)
}

func (s *Server) conversationViewsWithSearchDetails(ctx context.Context, userID int64, seeds []conversationSeed, own map[string]bool, query string) ([]conversationView, error) {
	return s.conversationViewsFromSeeds(ctx, userID, seeds, own, query)
}

func conversationSeedsFromMessages(messages []store.MessageRecord) []conversationSeed {
	seeds := make([]conversationSeed, 0, len(messages))
	for _, msg := range messages {
		seeds = append(seeds, conversationSeed{Message: msg})
	}
	return seeds
}

func stripStarSearchOperators(query string) (string, *bool) {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return query, nil
	}
	out := make([]string, 0, len(fields))
	var filter *bool
	changed := false
	for _, token := range fields {
		negated := strings.HasPrefix(token, "-")
		operator := strings.TrimPrefix(token, "-")
		var value bool
		switch strings.ToLower(operator) {
		case "is:starred":
			value = !negated
		case "is:notstarred":
			value = negated
		default:
			out = append(out, token)
			continue
		}
		filter = &value
		changed = true
	}
	if !changed {
		return query, nil
	}
	return strings.Join(out, " "), filter
}

func (s *Server) conversationViewsFromSeeds(ctx context.Context, userID int64, seeds []conversationSeed, own map[string]bool, query string) ([]conversationView, error) {
	type group struct {
		messages []store.MessageRecord
		seen     map[int64]bool
		seed     store.MessageRecord
		hasSeed  bool
		terms    []string
		fields   []string
	}
	threadKeys := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if key := strings.TrimSpace(seed.Message.ThreadKey); key != "" {
			threadKeys = append(threadKeys, key)
		}
	}
	threadsByKey, err := s.store.ListThreadMessagesByKeysForUser(ctx, userID, threadKeys)
	if err != nil {
		return nil, err
	}
	groups := map[string]*group{}
	order := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		thread := threadsByKey[strings.TrimSpace(seed.Message.ThreadKey)]
		if len(thread) == 0 {
			thread = []store.MessageRecord{seed.Message}
		}
		key := conversationListKey(thread, own)
		g := groups[key]
		if g == nil {
			g = &group{seen: map[int64]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		if !g.hasSeed {
			g.seed = seed.Message
			g.hasSeed = true
		}
		g.terms = mergeTerms(g.terms, seed.MatchTerms)
		g.fields = mergeFields(g.fields, seed.MatchFields)
		for _, msg := range thread {
			if g.seen[msg.ID] {
				continue
			}
			g.seen[msg.ID] = true
			g.messages = append(g.messages, msg)
		}
	}
	out := make([]conversationView, 0, len(order))
	for _, key := range order {
		group := groups[key]
		view := summarizeConversation(group.messages, own)
		if view.HasAttachments {
			view.AttachmentNames = s.conversationAttachmentNames(ctx, userID, group.messages, 3)
			if len(view.AttachmentNames) == 0 {
				view.HasAttachments = false
			}
		}
		if strings.TrimSpace(query) != "" && group.hasSeed {
			view.Snippet = searchResultSnippet(query, group.terms, group.seed, view.Snippet)
			view.MatchTerms = group.terms
			view.MatchFields = group.fields
			view.AttachmentMatches, view.AttachmentContentMatched = s.conversationAttachmentMatches(ctx, userID, group.messages, group.terms, group.fields, query)
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Server) searchConversationSeeds(ctx context.Context, userID int64, q string, sortMode search.SortMode, page, pageSize int, opts search.SearchOptions, own map[string]bool) ([]store.MessageRecord, error) {
	seeds, err := s.searchConversationSeedHits(ctx, userID, q, sortMode, page, pageSize, opts, own, searchMailboxFilter{}, nil)
	if err != nil {
		return nil, err
	}
	messages := make([]store.MessageRecord, 0, len(seeds))
	for _, seed := range seeds {
		messages = append(messages, seed.Message)
	}
	return messages, nil
}

func (s *Server) searchConversationSeedHits(ctx context.Context, userID int64, q string, sortMode search.SortMode, page, pageSize int, opts search.SearchOptions, own map[string]bool, mailboxFilter searchMailboxFilter, timing *searchTiming) ([]conversationSeed, error) {
	searchQuery, starFilter := stripStarSearchOperators(q)
	targetStart := (page - 1) * pageSize
	targetEnd := targetStart + pageSize + 1
	seen := map[string]bool{}
	unique := make([]conversationSeed, 0, targetEnd)
	rawOffset := 0
	const batchSize = 100
	for len(unique) < targetEnd {
		bleveStart := time.Now()
		hits, err := s.search.SearchHitsWithOptions(ctx, userID, searchQuery, sortMode, batchSize, rawOffset, opts)
		if timing != nil {
			timing.bleve += time.Since(bleveStart)
			timing.batches++
			timing.rawHits += len(hits)
		}
		if err != nil {
			return nil, err
		}
		if len(hits) == 0 {
			break
		}
		ids := make([]int64, 0, len(hits))
		termsByID := map[int64][]string{}
		fieldsByID := map[int64][]string{}
		for _, hit := range hits {
			ids = append(ids, hit.ID)
			termsByID[hit.ID] = hit.Terms
			fieldsByID[hit.ID] = hit.Fields
		}
		hydrateStart := time.Now()
		messages, err := s.store.ListMessagesByIDsForUser(ctx, userID, ids)
		if err != nil {
			if timing != nil {
				timing.hydrate += time.Since(hydrateStart)
			}
			return nil, err
		}
		for _, msg := range messages {
			if !mailboxFilter.matches(msg) {
				continue
			}
			if starFilter != nil && msg.IsStarred != *starFilter {
				continue
			}
			key := conversationListKey([]store.MessageRecord{msg}, own)
			if seen[key] {
				continue
			}
			seen[key] = true
			unique = append(unique, conversationSeed{Message: msg, MatchTerms: termsByID[msg.ID], MatchFields: fieldsByID[msg.ID]})
			if len(unique) >= targetEnd {
				break
			}
		}
		if timing != nil {
			timing.hydrate += time.Since(hydrateStart)
		}
		rawOffset += len(hits)
		if len(hits) < batchSize {
			break
		}
	}
	if targetStart >= len(unique) {
		return nil, nil
	}
	if targetEnd > len(unique) {
		targetEnd = len(unique)
	}
	return unique[targetStart:targetEnd], nil
}

func mergeTerms(existing []string, next []string) []string {
	if len(next) == 0 {
		return existing
	}
	seen := map[string]bool{}
	for _, term := range existing {
		seen[strings.ToLower(term)] = true
	}
	out := append([]string{}, existing...)
	for _, term := range next {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
		if len(out) >= 10 {
			break
		}
	}
	return out
}

func mergeFields(existing []string, next []string) []string {
	if len(next) == 0 {
		return existing
	}
	seen := map[string]bool{}
	for _, field := range existing {
		seen[field] = true
	}
	out := append([]string{}, existing...)
	for _, field := range next {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func summarizeConversation(thread []store.MessageRecord, own map[string]bool) conversationView {
	thread = dedupeConversationMessages(thread)
	latest := thread[0]
	starred := false
	starredMessage := store.MessageRecord{}
	allRead := true
	hasAttachments := false
	for _, msg := range thread {
		if msg.Date.After(latest.Date) || (msg.Date.Equal(latest.Date) && msg.ID > latest.ID) {
			latest = msg
		}
		if msg.IsStarred && (!starred || msg.Date.After(starredMessage.Date) || (msg.Date.Equal(starredMessage.Date) && msg.ID > starredMessage.ID)) {
			starred = true
			starredMessage = msg
		}
		if !msg.IsRead {
			allRead = false
		}
		if msg.HasAttachments {
			hasAttachments = true
		}
	}
	displayMessage := latest
	displayMessage.IsStarred = starred
	starredMessageID := latest.ID
	if starred {
		starredMessageID = starredMessage.ID
	}
	return conversationView{
		Message:          displayMessage,
		StarredMessageID: starredMessageID,
		Participants:     participantSummary(thread, own),
		Count:            len(thread),
		IsRead:           allRead,
		HasAttachments:   hasAttachments,
		Snippet:          messageSnippet(latest.BodyText, latest.BodyHTML),
	}
}

func (s *Server) conversationAttachmentNames(ctx context.Context, userID int64, messages []store.MessageRecord, limit int) []string {
	attachments, err := s.conversationAttachments(ctx, userID, messages, limit)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(attachments))
	for _, att := range attachments {
		name := strings.TrimSpace(att.Filename)
		if name == "" {
			name = strings.TrimSpace(att.ContentType)
		}
		if name == "" {
			name = "Attachment"
		}
		names = append(names, name)
	}
	return names
}

func (s *Server) conversationAttachmentMatches(ctx context.Context, userID int64, messages []store.MessageRecord, terms, fields []string, query string) ([]string, bool) {
	if !searchFieldsInclude(fields, "attachment_names", "attachments", "attachment_types") {
		return nil, false
	}
	attachments, err := s.conversationAttachments(ctx, userID, messages, 12)
	if err != nil {
		return nil, searchFieldsInclude(fields, "attachments")
	}
	needles := mergeSnippetTerms(terms, searchSnippetTerms(query))
	var matches []string
	if searchFieldsInclude(fields, "attachment_names") {
		for _, att := range attachments {
			name := strings.TrimSpace(att.Filename)
			if name == "" {
				continue
			}
			if attachmentNameMatches(name, needles) {
				matches = append(matches, name)
			}
		}
	}
	return uniqueStrings(matches, 4), searchFieldsInclude(fields, "attachments") && len(matches) == 0
}

func (s *Server) conversationAttachments(ctx context.Context, userID int64, messages []store.MessageRecord, limit int) ([]store.Attachment, error) {
	if limit <= 0 {
		limit = 3
	}
	var out []store.Attachment
	seen := map[string]bool{}
	for _, msg := range messages {
		if !msg.HasAttachments {
			continue
		}
		attachments, err := s.store.ListAttachmentsForMessage(ctx, userID, msg.ID)
		if err != nil {
			return nil, err
		}
		for _, att := range attachments {
			if !isDisplayAttachment(att) {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(att.Filename))
			if key == "" {
				key = fmt.Sprintf("%d", att.ID)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, att)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func searchFieldsInclude(fields []string, candidates ...string) bool {
	for _, field := range fields {
		for _, candidate := range candidates {
			if field == candidate {
				return true
			}
		}
	}
	return false
}

func attachmentNameMatches(name string, terms []string) bool {
	normalized := strings.ToLower(name)
	for _, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" {
			continue
		}
		if strings.Contains(normalized, term) {
			return true
		}
		for _, word := range strings.FieldsFunc(normalized, func(r rune) bool {
			return !isSearchWordRune(r)
		}) {
			if word == term {
				return true
			}
		}
	}
	return false
}

func isSearchWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127
}

func uniqueStrings(values []string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func dedupeConversationMessages(thread []store.MessageRecord) []store.MessageRecord {
	if len(thread) < 2 {
		return thread
	}
	out := make([]store.MessageRecord, 0, len(thread))
	seen := map[string]int{}
	for _, msg := range thread {
		key := conversationMessageIdentity(msg)
		if idx, ok := seen[key]; ok {
			existing := out[idx]
			previous := existing
			read := existing.IsRead && msg.IsRead
			starred := existing.IsStarred || msg.IsStarred
			hasAttachments := existing.HasAttachments || msg.HasAttachments
			if msg.Date.After(existing.Date) || (msg.Date.Equal(existing.Date) && msg.ID > existing.ID) {
				existing = msg
			}
			if starred && !existing.IsStarred {
				if previous.IsStarred {
					existing = previous
				} else if msg.IsStarred {
					existing = msg
				}
			}
			existing.IsRead = read
			existing.IsStarred = starred
			existing.HasAttachments = hasAttachments
			out[idx] = existing
			continue
		}
		seen[key] = len(out)
		out = append(out, msg)
	}
	return out
}

func conversationMessageIdentity(msg store.MessageRecord) string {
	if id := strings.ToLower(strings.TrimSpace(msg.MessageIDHeader)); id != "" {
		return "message-id:" + id
	}
	return fmt.Sprintf("local:%d", msg.ID)
}

func participantSummary(thread []store.MessageRecord, own map[string]bool) string {
	var labels []string
	seen := map[string]bool{}
	hasMe := false
	for _, msg := range thread {
		identity := store.SenderIdentity(msg.FromAddr)
		label := senderDisplayName(msg.FromAddr)
		if own[identity] {
			label = "me"
			hasMe = true
		}
		if label == "" {
			label = "Unknown sender"
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		labels = append(labels, label)
	}
	if hasMe && len(labels) > 1 {
		for i, label := range labels {
			if label == "me" {
				labels = append(labels[:i], labels[i+1:]...)
				labels = append(labels, "me")
				break
			}
		}
	}
	if len(labels) == 0 {
		return "Unknown sender"
	}
	if len(labels) > 3 {
		return fmt.Sprintf("%s, %s, %s +%d", labels[0], labels[1], labels[2], len(labels)-3)
	}
	return strings.Join(labels, ", ")
}

func senderDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if addrs, err := mail.ParseAddressList(value); err == nil && len(addrs) > 0 {
		if strings.TrimSpace(addrs[0].Name) != "" {
			return strings.TrimSpace(addrs[0].Name)
		}
		return strings.TrimSpace(addrs[0].Address)
	}
	return strings.Trim(value, "<> \t\r\n")
}

func senderEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if addrs, err := mail.ParseAddressList(value); err == nil && len(addrs) > 0 {
		return strings.TrimSpace(addrs[0].Address)
	}
	return strings.Trim(value, "<> \t\r\n")
}

func senderInitial(value string) string {
	label := senderDisplayName(value)
	if label == "" {
		label = senderEmail(value)
	}
	for _, r := range strings.TrimSpace(label) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func recipientLine(msg store.MessageRecord) string {
	to := strings.TrimSpace(msg.ToAddr)
	cc := strings.TrimSpace(msg.CCAddr)
	switch {
	case to != "" && cc != "":
		return "to " + to + ", cc " + cc
	case to != "":
		return "to " + to
	case cc != "":
		return "cc " + cc
	default:
		return "to undisclosed recipients"
	}
}

func conversationKey(msg store.MessageRecord) string {
	if strings.TrimSpace(msg.ThreadKey) != "" {
		return "thread:" + msg.ThreadKey
	}
	return fmt.Sprintf("message:%d", msg.ID)
}

func conversationListKey(messages []store.MessageRecord, own map[string]bool) string {
	if len(messages) == 0 {
		return ""
	}
	if key := reliableThreadBoundaryKey(messages); key != "" {
		return key
	}
	subject := ""
	ids := map[string]bool{}
	for _, msg := range messages {
		if subject == "" {
			subject = store.NormalizedThreadSubject(msg.Subject)
		}
		for _, value := range []string{msg.FromAddr, msg.ToAddr, msg.CCAddr} {
			for _, identity := range addressIdentities(value) {
				if own[identity] {
					identity = "me"
				}
				if identity != "" {
					ids[identity] = true
				}
			}
		}
	}
	if subject == "" {
		return conversationKey(messages[0])
	}
	parts := make([]string, 0, len(ids))
	for identity := range ids {
		parts = append(parts, identity)
	}
	sortStrings(parts)
	if len(parts) == 0 {
		return "subject:" + subject
	}
	return "subject:" + subject + "|people:" + strings.Join(parts, ",")
}

func reliableThreadBoundaryKey(messages []store.MessageRecord) string {
	key := reliableMessageIDThreadKey(messages[0])
	if key == "" {
		return ""
	}
	for _, msg := range messages[1:] {
		if reliableMessageIDThreadKey(msg) != key {
			return ""
		}
	}
	return "thread:" + key
}

func reliableMessageIDThreadKey(msg store.MessageRecord) string {
	key := strings.TrimSpace(msg.ThreadKey)
	if key == "" {
		key = store.ThreadKey(msg.MessageIDHeader, msg.InReplyTo, msg.ReferencesHeader, msg.Subject)
	}
	if strings.HasPrefix(key, "msgid:") {
		return key
	}
	return ""
}

func addressIdentities(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if addrs, err := mail.ParseAddressList(value); err == nil {
		out := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			identity := store.SenderIdentity(addr.Address)
			if identity != "" {
				out = append(out, identity)
			}
		}
		return out
	}
	if identity := store.SenderIdentity(value); identity != "" {
		return []string{identity}
	}
	return nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (s *Server) ownAddresses(ctx context.Context, user store.User) map[string]bool {
	own := map[string]bool{}
	for _, value := range []string{user.Email} {
		if identity := store.SenderIdentity(value); identity != "" {
			own[identity] = true
		}
	}
	if account, err := s.store.GetMailAccount(ctx, user.ID); err == nil {
		for _, value := range []string{account.Email, account.Username, account.SMTPUsername} {
			if identity := store.SenderIdentity(value); identity != "" {
				own[identity] = true
			}
		}
	}
	if emails, err := s.store.ListMeContactEmailsForUser(ctx, user.ID); err == nil {
		for _, value := range emails {
			if identity := store.SenderIdentity(value); identity != "" {
				own[identity] = true
			}
		}
	}
	return own
}

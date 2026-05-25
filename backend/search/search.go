// File overview: Bleve search service. It owns per-user indexes, document mapping, query construction, result ranking, and highlighting metadata.

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	blevesearch "github.com/blevesearch/bleve/v2/search"
	blevequery "github.com/blevesearch/bleve/v2/search/query"

	languagesearch "mailmirror/backend/plugins/language_search"
	"mailmirror/backend/store"
)

type Service struct {
	index   bleve.Index
	root    string
	perUser bool
	mu      sync.Mutex
	indexes map[int64]bleve.Index
}

type AttachmentDoc struct {
	Filename    string
	ContentType string
	Text        string
}

type SortMode string

const (
	SortBest   SortMode = "best"
	SortRecent SortMode = "recent"
)

const maxCompoundFieldBytes = 128 * 1024

type SearchOptions struct {
	SenderBoosts []SenderBoost
}

type Hit struct {
	ID     int64
	Terms  []string
	Fields []string
}

type SenderBoost struct {
	Sender string
	Boost  float64
}

func Open(path string) (*Service, error) {
	index, err := openIndex(path)
	if err != nil {
		return nil, err
	}
	return &Service{index: index}, nil
}

func OpenPerUser(root string) (*Service, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Service{root: root, perUser: true, indexes: make(map[int64]bleve.Index)}, nil
}

func openIndex(path string) (bleve.Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	index, err := bleve.Open(path)
	if err != nil {
		mapping := bleve.NewIndexMapping()
		mapping.StoreDynamic = false
		doc := bleve.NewDocumentMapping()
		userField := keywordField()
		doc.AddFieldMappingsAt("user_id", userField)
		doc.AddFieldMappingsAt("mailbox_id", userField)
		doc.AddFieldMappingsAt("has_attachment", userField)
		doc.AddFieldMappingsAt("is_read", userField)
		doc.AddFieldMappingsAt("is_starred", userField)
		doc.AddFieldMappingsAt("plugin_language_code", userField)
		for _, field := range []string{
			"subject", "subject_compound", "from", "from_compound", "from_domain", "to", "cc",
			"message_id", "body", "attachment_names", "attachment_types", "attachments", "compound",
		} {
			doc.AddFieldMappingsAt(field, textField())
		}
		doc.AddFieldMappingsAt("date", dateField())
		mapping.DefaultMapping = doc
		index, err = bleve.New(path, mapping)
		if err != nil {
			return nil, err
		}
	}
	return index, nil
}

func textField() *mapping.FieldMapping {
	field := bleve.NewTextFieldMapping()
	field.Store = false
	field.DocValues = false
	return field
}

func keywordField() *mapping.FieldMapping {
	field := bleve.NewKeywordFieldMapping()
	field.Store = false
	field.IncludeInAll = false
	return field
}

func dateField() *mapping.FieldMapping {
	field := bleve.NewDateTimeFieldMapping()
	field.Store = false
	field.IncludeInAll = false
	return field
}

func (s *Service) Close() error {
	var first error
	if s.index != nil {
		if err := s.index.Close(); err != nil {
			first = err
		}
	}
	s.mu.Lock()
	indexes := make([]bleve.Index, 0, len(s.indexes))
	for _, index := range s.indexes {
		indexes = append(indexes, index)
	}
	s.indexes = map[int64]bleve.Index{}
	s.mu.Unlock()
	for _, index := range indexes {
		if err := index.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Service) indexForUser(userID int64) (bleve.Index, error) {
	if !s.perUser {
		if s.index == nil {
			return nil, fmt.Errorf("search index is not open")
		}
		return s.index, nil
	}
	if userID == 0 {
		return nil, fmt.Errorf("user id is required for search index")
	}
	s.mu.Lock()
	if index := s.indexes[userID]; index != nil {
		s.mu.Unlock()
		return index, nil
	}
	s.mu.Unlock()

	index, err := openIndex(filepath.Join(s.root, strconv.FormatInt(userID, 10), "bleve"))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if existing := s.indexes[userID]; existing != nil {
		s.mu.Unlock()
		_ = index.Close()
		return existing, nil
	}
	s.indexes[userID] = index
	s.mu.Unlock()
	return index, nil
}

func (s *Service) IndexMessage(ctx context.Context, msg store.MessageRecord, attachments []AttachmentDoc) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	names := make([]string, 0, len(attachments))
	contentTypes := make([]string, 0, len(attachments))
	texts := make([]string, 0, len(attachments))
	for _, att := range attachments {
		names = append(names, att.Filename)
		contentTypes = append(contentTypes, att.ContentType)
		if strings.TrimSpace(att.Text) != "" {
			texts = append(texts, att.Text)
		}
	}
	hasAttachment := msg.HasAttachments || len(attachments) > 0
	compoundBody := store.MessageBodyPreview(msg.BodyText, maxCompoundFieldBytes/4)
	compoundAttachments := store.MessageBodyPreview(strings.Join(texts, " "), maxCompoundFieldBytes/4)
	doc := map[string]any{
		"user_id":              strconv.FormatInt(msg.UserID, 10),
		"mailbox_id":           strconv.FormatInt(msg.MailboxID, 10),
		"subject":              msg.Subject,
		"subject_compound":     compoundSearchText(msg.Subject),
		"from":                 msg.FromAddr,
		"from_compound":        compoundSearchText(msg.FromAddr),
		"from_domain":          emailDomainTerms(msg.FromAddr),
		"to":                   msg.ToAddr,
		"cc":                   msg.CCAddr,
		"message_id":           msg.MessageIDHeader,
		"body":                 msg.BodyText,
		"date":                 msg.Date,
		"is_read":              strconv.FormatBool(msg.IsRead),
		"is_starred":           strconv.FormatBool(msg.IsStarred),
		"plugin_language_code": languagesearch.NormalizeCode(msg.LanguageCode),
		"has_attachment":       strconv.FormatBool(hasAttachment),
		"attachment_names":     strings.Join(names, " "),
		"attachment_types":     strings.Join(contentTypes, " "),
		"attachments":          strings.Join(texts, " "),
		"compound": compoundSearchText(
			msg.Subject,
			msg.FromAddr,
			msg.ToAddr,
			msg.CCAddr,
			msg.MessageIDHeader,
			compoundBody,
			strings.Join(names, " "),
			compoundAttachments,
		),
	}
	index, err := s.indexForUser(msg.UserID)
	if err != nil {
		return err
	}
	return index.Index(strconv.FormatInt(msg.ID, 10), doc)
}

func (s *Service) DeleteMessage(ctx context.Context, userID, messageID int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	index, err := s.indexForUser(userID)
	if err != nil {
		return err
	}
	return index.Delete(strconv.FormatInt(messageID, 10))
}

func (s *Service) CountMailboxMessages(ctx context.Context, userID, mailboxID int64) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	if userID == 0 || mailboxID == 0 {
		return 0, nil
	}
	userQuery := bleve.NewTermQuery(strconv.FormatInt(userID, 10))
	userQuery.SetField("user_id")
	mailboxQuery := bleve.NewTermQuery(strconv.FormatInt(mailboxID, 10))
	mailboxQuery.SetField("mailbox_id")
	req := bleve.NewSearchRequestOptions(bleve.NewConjunctionQuery(userQuery, mailboxQuery), 0, 0, false)
	index, err := s.indexForUser(userID)
	if err != nil {
		return 0, err
	}
	res, err := index.Search(req)
	if err != nil {
		return 0, err
	}
	return int(res.Total), nil
}

func (s *Service) Search(ctx context.Context, userID int64, queryText string, sortMode SortMode, limit, offset int) ([]int64, error) {
	return s.SearchWithOptions(ctx, userID, queryText, sortMode, limit, offset, SearchOptions{})
}

func (s *Service) SearchWithOptions(ctx context.Context, userID int64, queryText string, sortMode SortMode, limit, offset int, opts SearchOptions) ([]int64, error) {
	res, err := s.search(ctx, userID, queryText, sortMode, limit, offset, opts, false)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(res.Hits))
	for _, hit := range res.Hits {
		id, err := strconv.ParseInt(hit.ID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse search hit id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Service) SearchHitsWithOptions(ctx context.Context, userID int64, queryText string, sortMode SortMode, limit, offset int, opts SearchOptions) ([]Hit, error) {
	res, err := s.search(ctx, userID, queryText, sortMode, limit, offset, opts, true)
	if err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(res.Hits))
	for _, hit := range res.Hits {
		id, err := strconv.ParseInt(hit.ID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse search hit id: %w", err)
		}
		hits = append(hits, Hit{ID: id, Terms: hitMatchTerms(queryText, hit.Locations), Fields: hitMatchFields(hit.Locations)})
	}
	return hits, nil
}

func (s *Service) MatchMessage(ctx context.Context, userID, messageID int64, queryText string) (Hit, bool, error) {
	select {
	case <-ctx.Done():
		return Hit{}, false, ctx.Err()
	default:
	}
	queryText = strings.TrimSpace(queryText)
	if userID == 0 || messageID == 0 || queryText == "" {
		return Hit{}, false, nil
	}
	docID := strconv.FormatInt(messageID, 10)
	docQuery := bleve.NewDocIDQuery([]string{docID})
	query := bleve.NewConjunctionQuery(buildQuery(userID, queryText, SortRecent, SearchOptions{}), docQuery)
	req := bleve.NewSearchRequestOptions(query, 1, 0, false)
	req.IncludeLocations = true
	index, err := s.indexForUser(userID)
	if err != nil {
		return Hit{}, false, err
	}
	res, err := index.Search(req)
	if err != nil {
		return Hit{}, false, err
	}
	if len(res.Hits) == 0 {
		return Hit{}, false, nil
	}
	hit := res.Hits[0]
	return Hit{ID: messageID, Terms: hitMatchTerms(queryText, hit.Locations), Fields: hitMatchFields(hit.Locations)}, true, nil
}

func (s *Service) search(ctx context.Context, userID int64, queryText string, sortMode SortMode, limit, offset int, opts SearchOptions, includeLocations bool) (*bleve.SearchResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := buildQuery(userID, queryText, sortMode, opts)
	req := bleve.NewSearchRequestOptions(query, limit, offset, false)
	req.IncludeLocations = includeLocations
	if sortMode == SortRecent {
		req.SortBy([]string{"-date"})
	}
	index, err := s.indexForUser(userID)
	if err != nil {
		return nil, err
	}
	return index.Search(req)
}

func buildQuery(userID int64, queryText string, sortMode SortMode, opts SearchOptions) blevequery.Query {
	parsed := parseQuery(queryText)
	parts := []blevequery.Query{}

	userQuery := bleve.NewTermQuery(strconv.FormatInt(userID, 10))
	userQuery.SetField("user_id")
	parts = append(parts, userQuery)

	if parsed.HasAttachment != nil {
		q := bleve.NewTermQuery(strconv.FormatBool(*parsed.HasAttachment))
		q.SetField("has_attachment")
		parts = append(parts, q)
	}
	if parsed.IsRead != nil {
		q := bleve.NewTermQuery(strconv.FormatBool(*parsed.IsRead))
		q.SetField("is_read")
		parts = append(parts, q)
	}
	if parsed.IsStarred != nil {
		q := bleve.NewTermQuery(strconv.FormatBool(*parsed.IsStarred))
		q.SetField("is_starred")
		parts = append(parts, q)
	}
	if parsed.Language != "" {
		pluginQ := bleve.NewTermQuery(parsed.Language)
		pluginQ.SetField("plugin_language_code")
		parts = append(parts, pluginQ)
	}
	if parsed.Filename != "" {
		q := bleve.NewMatchQuery(parsed.Filename)
		q.SetField("attachment_names")
		parts = append(parts, q)
	}
	if parsed.Subject != "" {
		q := bleve.NewMatchPhraseQuery(parsed.Subject)
		q.SetField("subject")
		parts = append(parts, q)
	}
	if parsed.From != "" {
		parts = append(parts, fromQuery(parsed.From))
	}
	if parsed.To != "" {
		q := bleve.NewMatchQuery(parsed.To)
		q.SetField("to")
		parts = append(parts, q)
	}
	if parsed.CC != "" {
		q := bleve.NewMatchQuery(parsed.CC)
		q.SetField("cc")
		parts = append(parts, q)
	}
	if !parsed.After.IsZero() || !parsed.Before.IsZero() {
		startInclusive := true
		endInclusive := false
		q := bleve.NewDateRangeInclusiveQuery(parsed.After, parsed.Before, &startInclusive, &endInclusive)
		q.SetField("date")
		parts = append(parts, q)
	}
	if parsed.Text == "" {
		if len(parts) == 1 {
			parts = append(parts, bleve.NewMatchAllQuery())
		}
	} else {
		parts = append(parts, textQuery(parsed.Text, parsed.TextQuoted))
	}
	must := bleve.NewConjunctionQuery(parts...)
	if sortMode != SortBest {
		return must
	}
	should := recencyBoostQueries(time.Now().UTC())
	should = append(should, senderBoostQueries(opts.SenderBoosts)...)
	if len(should) == 0 {
		return must
	}
	return blevequery.NewBooleanQuery([]blevequery.Query{must}, should, nil)
}

func senderBoostQueries(boosts []SenderBoost) []blevequery.Query {
	if len(boosts) == 0 {
		return nil
	}
	if len(boosts) > 40 {
		boosts = boosts[:40]
	}
	out := make([]blevequery.Query, 0, len(boosts))
	for _, boost := range boosts {
		sender := strings.TrimSpace(boost.Sender)
		if sender == "" || boost.Boost <= 0 {
			continue
		}
		q := bleve.NewMatchQuery(sender)
		q.SetField("from")
		q.SetBoost(boost.Boost)
		out = append(out, q)
	}
	return out
}

func fromQuery(text string) blevequery.Query {
	text = strings.Trim(strings.TrimSpace(text), `"`)
	if terms := emailAddressCompoundTerms(text); len(terms) > 0 {
		return boostQuery(termConjunction("from_compound", terms), 16)
	}
	var disjuncts []blevequery.Query

	domain := strings.TrimPrefix(strings.ToLower(text), "@")
	if at := strings.LastIndex(domain, "@"); at >= 0 {
		domain = domain[at+1:]
	}
	domainMode := false
	if terms := domainQueryTerms(domain); len(terms) > 1 {
		domainMode = true
		disjuncts = append(disjuncts, boostQuery(termConjunction("from", terms), 12))
		disjuncts = append(disjuncts, boostQuery(termConjunction("from_domain", terms), 18))
	} else {
		q := bleve.NewMatchQuery(text)
		q.SetField("from")
		q.SetBoost(8)
		disjuncts = append(disjuncts, q)
	}
	if normalized := normalizeSearchText(text); !domainMode && normalized != "" && normalized != text {
		nq := bleve.NewMatchQuery(normalized)
		nq.SetField("from")
		nq.SetBoost(4)
		disjuncts = append(disjuncts, nq)
	}
	return bleve.NewDisjunctionQuery(disjuncts...)
}

func textQuery(text string, quoted bool) blevequery.Query {
	var disjuncts []blevequery.Query
	normalized := normalizeSearchText(text)
	terms := strings.Fields(normalized)
	if quoted {
		for _, field := range literalTextFields() {
			phrase := bleve.NewMatchPhraseQuery(text)
			phrase.SetField(field)
			phrase.SetBoost(4)
			disjuncts = append(disjuncts, phrase)
			if len(terms) == 1 {
				q := bleve.NewMatchQuery(text)
				q.SetField(field)
				q.SetBoost(3)
				disjuncts = append(disjuncts, q)
				if normalized != "" && normalized != strings.ToLower(strings.TrimSpace(text)) {
					nq := bleve.NewMatchQuery(normalized)
					nq.SetField(field)
					nq.SetBoost(2)
					disjuncts = append(disjuncts, nq)
				}
			}
		}
		if len(disjuncts) == 1 {
			return disjuncts[0]
		}
		return bleve.NewDisjunctionQuery(disjuncts...)
	}
	if q := domainTextQuery(text); q != nil {
		disjuncts = append(disjuncts, q)
	} else if len(terms) > 1 {
		disjuncts = append(disjuncts, allTextTermsQuery(terms))
		phrase := bleve.NewMatchPhraseQuery(text)
		phrase.SetBoost(1.5)
		disjuncts = append(disjuncts, phrase)
	} else {
		disjuncts = append(disjuncts, bleve.NewQueryStringQuery(text))
		if normalized != "" && normalized != text {
			disjuncts = append(disjuncts, bleve.NewQueryStringQuery(normalized))
		}
	}
	joined := strings.ReplaceAll(normalized, " ", "")
	if joined != "" {
		if joined != normalized {
			disjuncts = append(disjuncts, boostQuery(bleve.NewQueryStringQuery(joined), 0.8))
		}
		disjuncts = append(disjuncts, exactCompactQueries(joined)...)
		disjuncts = append(disjuncts, splitPhraseQueries(joined)...)
		disjuncts = append(disjuncts, splitWordQueries(joined)...)
		if len(joined) >= 5 {
			fq := bleve.NewFuzzyQuery(joined)
			fq.SetField("compound")
			fq.SetFuzziness(fuzzinessFor(joined))
			fq.SetBoost(0.6)
			disjuncts = append(disjuncts, fq)
		}
	}
	if len(terms) <= 1 {
		for _, term := range terms {
			if len(term) >= 5 {
				q := bleve.NewFuzzyQuery(term)
				q.SetFuzziness(fuzzinessFor(term))
				q.SetBoost(0.5)
				disjuncts = append(disjuncts, q)
			}
		}
	}
	if len(disjuncts) == 1 {
		return disjuncts[0]
	}
	return bleve.NewDisjunctionQuery(disjuncts...)
}

func literalTextFields() []string {
	return []string{"subject", "from", "to", "cc", "message_id", "body", "attachment_names", "attachment_types", "attachments"}
}

func allTextTermsQuery(terms []string) blevequery.Query {
	parts := make([]blevequery.Query, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, textTermQuery(term))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return bleve.NewConjunctionQuery(parts...)
}

func textTermQuery(term string) blevequery.Query {
	queries := []blevequery.Query{bleve.NewMatchQuery(term)}
	if len(term) >= 5 {
		q := bleve.NewFuzzyQuery(term)
		q.SetFuzziness(fuzzinessFor(term))
		q.SetBoost(0.5)
		queries = append(queries, q)
	}
	if len(queries) == 1 {
		return queries[0]
	}
	return bleve.NewDisjunctionQuery(queries...)
}

func hitMatchTerms(queryText string, locations blevesearch.FieldTermLocationMap) []string {
	if len(locations) == 0 {
		return nil
	}
	needles := queryNeedles(parseQuery(queryText))
	seen := map[string]bool{}
	var terms []string
	for field, termLocations := range locations {
		if !matchTermField(field) {
			continue
		}
		for term := range termLocations {
			term = strings.TrimSpace(strings.ToLower(term))
			if term == "" || seen[term] || !termRelevantToQuery(term, needles) {
				continue
			}
			seen[term] = true
			terms = append(terms, term)
		}
	}
	sort.Slice(terms, func(i, j int) bool {
		if len([]rune(terms[i])) == len([]rune(terms[j])) {
			return terms[i] < terms[j]
		}
		return len([]rune(terms[i])) > len([]rune(terms[j]))
	})
	if len(terms) > 10 {
		terms = terms[:10]
	}
	return terms
}

func matchTermField(field string) bool {
	switch field {
	case "subject", "subject_compound", "from", "from_compound", "from_domain", "to", "cc", "message_id", "body", "attachment_names", "attachment_types", "attachments", "compound":
		return true
	default:
		return false
	}
}

func hitMatchFields(locations blevesearch.FieldTermLocationMap) []string {
	if len(locations) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var fields []string
	for field := range locations {
		if !matchTermField(field) || seen[field] {
			continue
		}
		seen[field] = true
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

type queryNeedle struct {
	Term        string
	MaxDistance int
}

func queryNeedles(parsed parsedQuery) []queryNeedle {
	seen := map[string]bool{}
	var needles []queryNeedle
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(strings.ToLower(value)), `"`)
		if value == "" {
			return
		}
		addNeedle := func(term string) {
			term = strings.TrimSpace(strings.ToLower(term))
			if term == "" || seen[term] {
				return
			}
			seen[term] = true
			maxDistance := 0
			if len(term) >= 5 {
				maxDistance = fuzzinessFor(term)
			}
			needles = append(needles, queryNeedle{Term: term, MaxDistance: maxDistance})
		}
		addNeedle(value)
		normalized := normalizeSearchText(value)
		for _, term := range strings.Fields(normalized) {
			addNeedle(term)
		}
		if joined := strings.ReplaceAll(normalized, " ", ""); joined != "" {
			addNeedle(joined)
		}
	}
	for _, value := range []string{parsed.Text, parsed.Filename, parsed.From, parsed.To, parsed.CC, parsed.Subject} {
		add(value)
	}
	return needles
}

func termRelevantToQuery(term string, needles []queryNeedle) bool {
	if len(needles) == 0 {
		return false
	}
	for _, needle := range needles {
		if term == needle.Term {
			return true
		}
		if len(term) >= 3 && len(needle.Term) >= 3 && (strings.Contains(term, needle.Term) || strings.Contains(needle.Term, term)) {
			return true
		}
		if needle.MaxDistance > 0 && boundedEditDistance(term, needle.Term, needle.MaxDistance) <= needle.MaxDistance {
			return true
		}
	}
	return false
}

func boundedEditDistance(a, b string, max int) int {
	if max < 0 {
		return max + 1
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar)-len(br) > max || len(br)-len(ar) > max {
		return max + 1
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ra := range ar {
		curr[0] = i + 1
		rowMin := curr[0]
		for j, rb := range br {
			cost := 1
			if ra == rb {
				cost = 0
			}
			curr[j+1] = minInt(curr[j]+1, prev[j+1]+1, prev[j]+cost)
			if curr[j+1] < rowMin {
				rowMin = curr[j+1]
			}
		}
		if rowMin > max {
			return max + 1
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func minInt(values ...int) int {
	out := values[0]
	for _, value := range values[1:] {
		if value < out {
			out = value
		}
	}
	return out
}

func domainTextQuery(text string) blevequery.Query {
	terms := domainQueryTerms(text)
	if len(terms) < 2 {
		return nil
	}
	return bleve.NewDisjunctionQuery(
		boostQuery(termConjunction("", terms), 1),
		boostQuery(termConjunction("from_domain", terms), 15),
	)
}

func domainQueryTerms(text string) []string {
	text = strings.Trim(strings.TrimSpace(text), `"`)
	text = strings.TrimPrefix(text, "@")
	if at := strings.LastIndex(text, "@"); at >= 0 {
		text = text[at+1:]
	}
	if strings.ContainsAny(text, " \t\r\n") || !strings.Contains(text, ".") {
		return nil
	}
	return strings.Fields(normalizeSearchText(text))
}

func termConjunction(field string, terms []string) blevequery.Query {
	parts := make([]blevequery.Query, 0, len(terms))
	for _, term := range terms {
		q := bleve.NewMatchQuery(term)
		if field != "" {
			q.SetField(field)
		}
		parts = append(parts, q)
	}
	return bleve.NewConjunctionQuery(parts...)
}

func recencyBoostQueries(now time.Time) []blevequery.Query {
	if now.IsZero() {
		return nil
	}
	buckets := []struct {
		age   time.Duration
		boost float64
	}{
		{36 * time.Hour, 35},
		{7 * 24 * time.Hour, 18},
		{30 * 24 * time.Hour, 9},
		{180 * 24 * time.Hour, 4},
		{730 * 24 * time.Hour, 1.5},
	}
	out := make([]blevequery.Query, 0, len(buckets))
	for _, bucket := range buckets {
		q := bleve.NewDateRangeQuery(now.Add(-bucket.age), time.Time{})
		q.SetField("date")
		q.SetBoost(bucket.boost)
		out = append(out, q)
	}
	return out
}

func exactCompactQueries(joined string) []blevequery.Query {
	if joined == "" {
		return nil
	}
	return []blevequery.Query{
		boostedMatch("subject_compound", joined, 80),
		boostedMatch("from_domain", joined, 70),
		boostedMatch("from", joined, 60),
		boostedMatch("from_compound", joined, 50),
		boostedMatch("compound", joined, 45),
	}
}

func splitPhraseQueries(term string) []blevequery.Query {
	if strings.Contains(term, " ") || len(term) < 6 || len(term) > 40 {
		return nil
	}
	var out []blevequery.Query
	for split := 3; split <= len(term)-3; split++ {
		phrase := term[:split] + " " + term[split:]
		out = append(out, boostedPhrase("subject", phrase, 35))
		out = append(out, boostedPhrase("body", phrase, 42))
		out = append(out, boostedPhrase("attachments", phrase, 24))
		out = append(out, boostedPhrase("from", phrase, 20))
	}
	return out
}

func splitWordQueries(term string) []blevequery.Query {
	if strings.Contains(term, " ") || len(term) < 6 || len(term) > 40 {
		return nil
	}
	var out []blevequery.Query
	for split := 3; split <= len(term)-3; split++ {
		out = append(out, boostQuery(bleve.NewConjunctionQuery(splitTermQuery(term[:split]), splitTermQuery(term[split:])), 0.2))
	}
	return out
}

func splitTermQuery(term string) blevequery.Query {
	queries := []blevequery.Query{bleve.NewMatchQuery(term)}
	if len(term) >= 4 {
		q := bleve.NewFuzzyQuery(term)
		q.SetFuzziness(1)
		q.SetBoost(0.5)
		queries = append(queries, q)
	}
	return bleve.NewDisjunctionQuery(queries...)
}

func boostedMatch(field, text string, boost float64) blevequery.Query {
	q := bleve.NewMatchQuery(text)
	q.SetField(field)
	q.SetBoost(boost)
	return q
}

func boostedPhrase(field, text string, boost float64) blevequery.Query {
	q := bleve.NewMatchPhraseQuery(text)
	q.SetField(field)
	q.SetBoost(boost)
	return q
}

func boostQuery(q blevequery.Query, boost float64) blevequery.Query {
	if q == nil {
		return nil
	}
	if bq, ok := q.(interface{ SetBoost(float64) }); ok {
		bq.SetBoost(boost)
	}
	return q
}

func fuzzinessFor(term string) int {
	return 1
}

func normalizeSearchText(text string) string {
	var b strings.Builder
	var lastSpace bool
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

func compoundSearchText(values ...string) string {
	normalized := normalizeSearchText(strings.Join(values, " "))
	words := strings.Fields(normalized)
	if len(words) < 2 {
		return ""
	}
	var b strings.Builder
	appendTerm := func(term string) bool {
		if term == "" {
			return true
		}
		if b.Len()+len(term)+1 > maxCompoundFieldBytes {
			return false
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(term)
		return true
	}
	for i := 0; i < len(words)-1; i++ {
		if !appendTerm(words[i] + words[i+1]) {
			return b.String()
		}
		if i+2 < len(words) {
			if !appendTerm(words[i] + words[i+1] + words[i+2]) {
				return b.String()
			}
		}
	}
	return b.String()
}

var emailAddressRE = regexp.MustCompile(`(?i)([a-z0-9._%+\-]+)@([a-z0-9.\-]+\.[a-z0-9\-]+)`)
var emailDomainRE = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@([a-z0-9.\-]+\.[a-z0-9\-]+)`)

func emailAddressQueryTerms(value string) []string {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	match := emailAddressRE.FindStringSubmatch(value)
	if len(match) != 3 {
		return nil
	}
	return strings.Fields(normalizeSearchText(match[1] + " " + match[2]))
}

func emailAddressCompoundTerms(value string) []string {
	terms := emailAddressQueryTerms(value)
	if len(terms) < 2 {
		return nil
	}
	return compactAdjacentTerms(terms)
}

func emailDomainTerms(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	seen := map[string]bool{}
	var out []string
	add := func(term string) {
		term = strings.Trim(strings.TrimSpace(term), "<>.,;:\"'()[]")
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		out = append(out, term)
	}
	matches := emailDomainRE.FindAllStringSubmatch(value, -1)
	for _, m := range matches {
		addDomainTerms(add, m[1])
	}
	if len(matches) == 0 {
		addDomainTerms(add, strings.TrimPrefix(value, "@"))
	}
	return strings.Join(out, " ")
}

func compactAdjacentTerms(words []string) []string {
	out := make([]string, 0, len(words)*2)
	for i := 0; i < len(words)-1; i++ {
		out = append(out, words[i]+words[i+1])
		if i+2 < len(words) {
			out = append(out, words[i]+words[i+1]+words[i+2])
		}
	}
	return out
}

func addDomainTerms(add func(string), domain string) {
	domain = strings.Trim(strings.ToLower(domain), "<>.,;:\"'()[]")
	if domain == "" {
		return
	}
	add(domain)
	normalized := normalizeSearchText(domain)
	add(normalized)
	add(strings.ReplaceAll(normalized, " ", ""))
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts); i++ {
		add(strings.Join(parts[i:], "."))
	}
}

type parsedQuery struct {
	Text          string
	TextQuoted    bool
	HasAttachment *bool
	IsRead        *bool
	IsStarred     *bool
	Language      string
	Filename      string
	From          string
	To            string
	CC            string
	Subject       string
	After         time.Time
	Before        time.Time
}

var operatorRE = regexp.MustCompile(`(?i)(^|\s)(-?)(has:attachment|is:read|is:unread|is:starred|is:notstarred|lang:("[^"]+"|\S+)|filename:("[^"]+"|\S+)|from:("[^"]+"|\S+)|to:("[^"]+"|\S+)|cc:("[^"]+"|\S+)|subject:("[^"]+"|\S+)|after:("[^"]+"|\S+)|before:("[^"]+"|\S+)|year:("[^"]+"|\S+))`)

func parseQuery(queryText string) parsedQuery {
	text, quoted := parseQueryText(queryText)
	out := parsedQuery{Text: text, TextQuoted: quoted}
	matches := operatorRE.FindAllStringSubmatchIndex(queryText, -1)
	if len(matches) == 0 {
		return out
	}
	var cleaned strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > last {
			cleaned.WriteString(queryText[last:start])
		}
		token := strings.TrimSpace(queryText[start:end])
		negated := strings.HasPrefix(token, "-")
		token = strings.TrimPrefix(token, "-")
		lower := strings.ToLower(token)
		switch {
		case lower == "has:attachment":
			v := !negated
			out.HasAttachment = &v
		case lower == "is:read":
			v := !negated
			out.IsRead = &v
		case lower == "is:unread":
			v := negated
			out.IsRead = &v
		case lower == "is:starred":
			v := !negated
			out.IsStarred = &v
		case lower == "is:notstarred":
			v := negated
			out.IsStarred = &v
		case strings.HasPrefix(lower, "lang:"):
			out.Language = languagesearch.NormalizeCode(strings.Trim(operatorValue(token), `"`))
		case strings.HasPrefix(lower, "filename:"):
			out.Filename = strings.Trim(operatorValue(token), `"`)
		case strings.HasPrefix(lower, "from:"):
			out.From = strings.Trim(operatorValue(token), `"`)
		case strings.HasPrefix(lower, "to:"):
			out.To = strings.Trim(operatorValue(token), `"`)
		case strings.HasPrefix(lower, "cc:"):
			out.CC = strings.Trim(operatorValue(token), `"`)
		case strings.HasPrefix(lower, "subject:"):
			out.Subject = strings.Trim(operatorValue(token), `"`)
		case strings.HasPrefix(lower, "after:"):
			out.After = parseSearchDate(operatorValue(token), false)
		case strings.HasPrefix(lower, "before:"):
			out.Before = parseSearchDate(operatorValue(token), false)
		case strings.HasPrefix(lower, "year:"):
			if start, end := parseSearchYear(operatorValue(token)); !start.IsZero() {
				out.After = start
				out.Before = end
			}
		}
		last = end
	}
	if last < len(queryText) {
		cleaned.WriteString(queryText[last:])
	}
	out.Text, out.TextQuoted = parseQueryText(cleaned.String())
	return out
}

func parseQueryText(value string) (string, bool) {
	text := strings.TrimSpace(value)
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		return strings.TrimSpace(text[1 : len(text)-1]), true
	}
	return text, false
}

func parseSearchDate(value string, endOfDay bool) time.Time {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if value == "" {
		return time.Time{}
	}
	now := time.Now().UTC()
	switch strings.ToLower(value) {
	case "today":
		return dayBoundary(now, endOfDay)
	case "yesterday":
		return dayBoundary(now.AddDate(0, 0, -1), endOfDay)
	}
	for _, layout := range []string{"2006/01/02", "2006-01-02", "01/02/2006"} {
		if t, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return dayBoundary(t, endOfDay)
		}
	}
	return time.Time{}
}

func parseSearchYear(value string) (time.Time, time.Time) {
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if len(value) != 4 {
		return time.Time{}, time.Time{}
	}
	year, err := strconv.Atoi(value)
	if err != nil || year < 1 || year > 9999 {
		return time.Time{}, time.Time{}
	}
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(1, 0, 0)
}

func dayBoundary(t time.Time, endOfDay bool) time.Time {
	y, m, d := t.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if !endOfDay {
		return start
	}
	return start.Add(24 * time.Hour)
}

func operatorValue(token string) string {
	if idx := strings.Index(token, ":"); idx >= 0 && idx+1 < len(token) {
		return token[idx+1:]
	}
	return ""
}

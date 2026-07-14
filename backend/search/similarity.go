// File overview: Bounded, tenant-scoped sparse similarity for backend plugins.
// SQLite selects and validates candidate envelopes; Bleve ranks only those IDs.

package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	blevesearch "github.com/blevesearch/bleve/v2/search"
	blevequery "github.com/blevesearch/bleve/v2/search/query"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const (
	maxSimilarityTermRunes = 128
	maxSimilarityWeight    = 100
	recentReadWindow       = 90 * 24 * time.Hour
)

type normalizedSimilarityTerm struct {
	field  string
	text   string
	weight float64
}

// SimilarMessages resolves one bounded candidate source through SQLite, removes
// the current message/thread and caller exclusions, and returns tenant-owned
// envelopes ranked by weighted Bleve term matches.
func (s *Service) SimilarMessages(ctx context.Context, db *store.Store, userID int64, request plugins.SimilarMessagesRequest) ([]plugins.SimilarMessageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || db == nil {
		return nil, errors.New("message similarity is not configured")
	}
	if userID <= 0 {
		return nil, errors.New("user id is required for message similarity")
	}
	if len(request.CandidateMessageIDs) > 0 && request.RecentRead != nil {
		return nil, errors.New("message similarity requires exactly one candidate source")
	}
	terms := normalizeSimilarityTerms(request.Terms)
	if len(terms) == 0 {
		return nil, nil
	}

	var current store.MessageSimilarityCandidate
	if request.CurrentMessageID > 0 {
		items, err := db.MessageSimilarityCandidatesForUser(ctx, userID, []int64{request.CurrentMessageID})
		if err != nil {
			return nil, fmt.Errorf("resolve current similarity message: %w", err)
		}
		if len(items) != 1 {
			return nil, fmt.Errorf("resolve current similarity message: %w", store.ErrNotFound)
		}
		current = items[0]
	}

	var candidates []store.MessageSimilarityCandidate
	var err error
	switch {
	case request.RecentRead != nil:
		since := request.RecentRead.Since.UTC()
		oldestAllowed := time.Now().UTC().Add(-recentReadWindow)
		if since.IsZero() || since.Before(oldestAllowed) {
			since = oldestAllowed
		}
		limit := request.RecentRead.Limit
		if limit <= 0 || limit > plugins.MaxSimilarityRecentReadCandidates {
			limit = plugins.MaxSimilarityRecentReadCandidates
		}
		candidates, err = db.RecentReadMessageSimilarityCandidatesForUser(ctx, userID, since, limit)
	case len(request.CandidateMessageIDs) > 0:
		ids := boundedUniqueIDs(request.CandidateMessageIDs, plugins.MaxSimilarityExplicitCandidates)
		candidates, err = db.MessageSimilarityCandidatesForUser(ctx, userID, ids)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve similarity candidates: %w", err)
	}

	excluded := make(map[int64]bool, min(len(request.ExcludeMessageIDs)+1, plugins.MaxSimilarityRecentReadCandidates+1))
	for _, id := range boundedUniqueIDs(request.ExcludeMessageIDs, plugins.MaxSimilarityRecentReadCandidates) {
		excluded[id] = true
	}
	if current.ID > 0 {
		excluded[current.ID] = true
	}
	owned := make(map[int64]store.MessageSimilarityCandidate, len(candidates))
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.UserID != userID || excluded[candidate.ID] {
			continue
		}
		if current.ThreadKey != "" && candidate.ThreadKey == current.ThreadKey {
			continue
		}
		if _, exists := owned[candidate.ID]; exists {
			continue
		}
		owned[candidate.ID] = candidate
		ids = append(ids, candidate.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	limit := request.Limit
	if limit <= 0 || limit > plugins.MaxSimilarityResults {
		limit = plugins.MaxSimilarityResults
	}
	hits, err := s.searchSimilarMessageIDs(ctx, userID, ids, terms, limit)
	if err != nil {
		return nil, err
	}
	out := make([]plugins.SimilarMessageResult, 0, len(hits))
	for _, hit := range hits {
		candidate, ok := owned[hit.id]
		if !ok || candidate.UserID != userID {
			continue
		}
		out = append(out, plugins.SimilarMessageResult{
			MessageID:            hit.id,
			Score:                hit.score,
			MatchedTerms:         hit.matchedTerms,
			MatchedTermCount:     hit.matchedTermCount,
			MatchedFields:        hit.matchedFields,
			WeightedTermCoverage: hit.weightedCoverage,
			Date:                 candidate.Date,
			From:                 candidate.FromAddr,
			ThreadKey:            candidate.ThreadKey,
		})
	}
	return out, nil
}

type similarityHit struct {
	id               int64
	score            float64
	matchedTerms     []string
	matchedTermCount int
	matchedFields    []string
	weightedCoverage float64
}

func (s *Service) searchSimilarMessageIDs(ctx context.Context, userID int64, candidateIDs []int64, terms []normalizedSimilarityTerm, limit int) ([]similarityHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	docIDs := make([]string, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		docIDs = append(docIDs, strconv.FormatInt(id, 10))
	}
	termQueries := make([]blevequery.Query, 0, len(terms))
	for _, term := range terms {
		query := bleve.NewMatchQuery(term.text)
		query.SetField(term.field)
		query.SetBoost(term.weight)
		if term.field == plugins.SimilarityFieldFromDomain && len(strings.Fields(term.text)) > 1 {
			query.SetOperator(blevequery.MatchQueryOperatorAnd)
		}
		termQueries = append(termQueries, query)
	}
	userQuery := bleve.NewTermQuery(strconv.FormatInt(userID, 10))
	userQuery.SetField("user_id")
	query := bleve.NewConjunctionQuery(
		userQuery,
		bleve.NewDocIDQuery(docIDs),
		bleve.NewDisjunctionQuery(termQueries...),
	)
	req := bleve.NewSearchRequestOptions(query, limit, 0, false)
	req.IncludeLocations = true
	index, err := s.indexForUser(userID)
	if err != nil {
		return nil, err
	}
	result, err := index.Search(req)
	if err != nil {
		return nil, err
	}
	hits := make([]similarityHit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		id, err := strconv.ParseInt(hit.ID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse similarity hit id: %w", err)
		}
		matchedTerms, matchedFields := similarityLocationSummary(hit.Locations)
		hits = append(hits, similarityHit{
			id:               id,
			score:            hit.Score,
			matchedTerms:     matchedTerms,
			matchedTermCount: similarityMatchedTermCount(terms, hit.Locations),
			matchedFields:    matchedFields,
			weightedCoverage: similarityWeightedCoverage(terms, hit.Locations),
		})
	}
	return hits, nil
}

func normalizeSimilarityTerms(input []plugins.SimilarityTerm) []normalizedSimilarityTerm {
	if len(input) > plugins.MaxSimilarityTerms {
		input = input[:plugins.MaxSimilarityTerms]
	}
	out := make([]normalizedSimilarityTerm, 0, len(input))
	positions := make(map[string]int, len(input))
	for _, term := range input {
		field := strings.TrimSpace(term.Field)
		if !similarityFieldAllowed(field) || term.Weight <= 0 || math.IsNaN(term.Weight) || math.IsInf(term.Weight, 0) {
			continue
		}
		text := normalizeSearchText(term.Text)
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > maxSimilarityTermRunes {
			text = strings.TrimSpace(string(runes[:maxSimilarityTermRunes]))
		}
		weight := min(term.Weight, float64(maxSimilarityWeight))
		key := field + "\x00" + text
		if position, exists := positions[key]; exists {
			out[position].weight = min(out[position].weight+weight, float64(maxSimilarityWeight))
			continue
		}
		positions[key] = len(out)
		out = append(out, normalizedSimilarityTerm{field: field, text: text, weight: weight})
	}
	return out
}

func similarityFieldAllowed(field string) bool {
	switch field {
	case plugins.SimilarityFieldSubject, plugins.SimilarityFieldFromDomain, plugins.SimilarityFieldBody:
		return true
	default:
		return false
	}
}

func similarityWeightedCoverage(terms []normalizedSimilarityTerm, locations blevesearch.FieldTermLocationMap) float64 {
	var matched, total float64
	for _, term := range terms {
		total += term.weight
		if similarityTermMatched(term, locations[term.field]) {
			matched += term.weight
		}
	}
	if total == 0 {
		return 0
	}
	return matched / total
}

func similarityMatchedTermCount(terms []normalizedSimilarityTerm, locations blevesearch.FieldTermLocationMap) int {
	count := 0
	for _, term := range terms {
		if similarityTermMatched(term, locations[term.field]) {
			count++
		}
	}
	return count
}

func similarityTermMatched(term normalizedSimilarityTerm, locations blevesearch.TermLocationMap) bool {
	if len(locations) == 0 {
		return false
	}
	needles := make(map[string]bool)
	for _, needle := range strings.Fields(term.text) {
		needles[needle] = true
	}
	matched := make(map[string]bool, len(needles))
	for located := range locations {
		for _, normalized := range strings.Fields(normalizeSearchText(located)) {
			if needles[normalized] {
				if term.field != plugins.SimilarityFieldFromDomain {
					return true
				}
				matched[normalized] = true
			}
		}
	}
	return term.field == plugins.SimilarityFieldFromDomain && len(matched) == len(needles)
}

func similarityLocationSummary(locations blevesearch.FieldTermLocationMap) ([]string, []string) {
	termSet := make(map[string]bool)
	fieldSet := make(map[string]bool)
	for field, fieldLocations := range locations {
		if !similarityFieldAllowed(field) || len(fieldLocations) == 0 {
			continue
		}
		fieldSet[field] = true
		for term := range fieldLocations {
			term = strings.TrimSpace(strings.ToLower(term))
			if term != "" {
				termSet[term] = true
			}
		}
	}
	terms := make([]string, 0, len(termSet))
	for term := range termSet {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return terms, fields
}

func boundedUniqueIDs(ids []int64, limit int) []int64 {
	out := make([]int64, 0, min(len(ids), limit))
	seen := make(map[int64]bool, cap(out))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == limit {
			break
		}
	}
	return out
}

// File overview: Bridges authenticated user profile fields to Bleve search options.
// Search handlers call this with the current session user, so query-time knobs
// live in request memory and do not require an extra database lookup before the
// request reaches the search index. The only optional lookups here read compact
// ranking hint tables: familiar senders and contact-book email addresses.

package web

import (
	"context"
	"strings"

	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

func searchOptionsForUser(user store.User) search.SearchOptions {
	senderBoost := user.SearchSenderBoost
	compactSplitting := user.SearchCompactSplitting
	legacyDefaults := user.SearchPreset == "" && user.SearchRecencyBias == "" && user.SearchFuzzy == "" && user.SearchAttachmentWeight == ""
	if legacyDefaults {
		senderBoost = true
		compactSplitting = true
	}
	return search.SearchOptions{Behavior: search.SearchBehavior{
		Preset:              user.SearchPreset,
		RecencyBias:         user.SearchRecencyBias,
		Fuzzy:               user.SearchFuzzy,
		SenderBoost:         senderBoost,
		SenderBoostSet:      true,
		SenderBoostScale:    searchWeightScale(searchSenderHistoryWeightForUser(user)),
		ContactBoostScale:   searchWeightScale(searchContactBoostWeightForUser(user)),
		AttachmentWeight:    user.SearchAttachmentWeight,
		CompactSplitting:    compactSplitting,
		CompactSplittingSet: true,
	}}
}

func (s *Server) searchOptionsWithRankingBoosts(ctx context.Context, user store.User) search.SearchOptions {
	opts := searchOptionsForUser(user)
	if opts.Behavior.SenderBoostScale > 0 {
		if stats, err := s.store.ListReadSenderStatsForUser(ctx, user.ID, 40); err == nil {
			for _, stat := range stats {
				opts.SenderBoosts = append(opts.SenderBoosts, search.SenderBoost{Sender: stat.Sender, Boost: stat.Boost})
			}
		}
	}
	if opts.Behavior.ContactBoostScale > 0 {
		if emails, err := s.store.ListContactEmailsForSearchBoostForUser(ctx, user.ID, 200); err == nil {
			for _, email := range emails {
				opts.ContactBoosts = append(opts.ContactBoosts, search.SenderBoost{Sender: email, Boost: 1})
			}
		}
	}
	return opts
}

func searchSenderBoostEnabledForUser(user store.User) bool {
	return searchWeightScale(searchSenderHistoryWeightForUser(user)) > 0
}

func searchSenderHistoryWeightForUser(user store.User) string {
	value := strings.ToLower(strings.TrimSpace(user.SearchSenderHistory))
	if value != "" {
		return value
	}
	if user.SearchPreset == "" && user.SearchRecencyBias == "" && user.SearchFuzzy == "" && user.SearchAttachmentWeight == "" {
		return "normal"
	}
	if !user.SearchSenderBoost {
		return "none"
	}
	return "normal"
}

func searchContactBoostWeightForUser(user store.User) string {
	value := strings.ToLower(strings.TrimSpace(user.SearchContactBoost))
	if value == "" {
		return "normal"
	}
	return value
}

func searchWeightScale(value string) float64 {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "off":
		return 0
	case "light":
		return 0.4
	case "strong":
		return 1.6
	default:
		return 1
	}
}

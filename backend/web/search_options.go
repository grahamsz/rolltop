// File overview: Bridges authenticated user profile fields to Bleve search options.
// Search handlers call this with the current session user, so query-time knobs
// live in request memory and do not require an extra database lookup before the
// request reaches the search index.

package web

import (
	"mailmirror/backend/search"
	"mailmirror/backend/store"
)

func searchOptionsForUser(user store.User) search.SearchOptions {
	senderBoost := user.SearchSenderBoost
	compactSplitting := user.SearchCompactSplitting
	if user.SearchPreset == "" && user.SearchRecencyBias == "" && user.SearchFuzzy == "" && user.SearchAttachmentWeight == "" {
		senderBoost = true
		compactSplitting = true
	}
	return search.SearchOptions{Behavior: search.SearchBehavior{
		Preset:              user.SearchPreset,
		RecencyBias:         user.SearchRecencyBias,
		Fuzzy:               user.SearchFuzzy,
		SenderBoost:         senderBoost,
		SenderBoostSet:      true,
		AttachmentWeight:    user.SearchAttachmentWeight,
		CompactSplitting:    compactSplitting,
		CompactSplittingSet: true,
	}}
}

func searchSenderBoostEnabledForUser(user store.User) bool {
	if user.SearchPreset == "" && user.SearchRecencyBias == "" && user.SearchFuzzy == "" && user.SearchAttachmentWeight == "" {
		return true
	}
	return user.SearchSenderBoost
}

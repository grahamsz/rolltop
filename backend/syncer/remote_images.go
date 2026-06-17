// File overview: Background remote image warm-cache scheduling during sync.

package syncer

import (
	"context"
	"strings"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/remoteimages"
	"rolltop/backend/store"
)

const remoteImageWarmRecentWindow = 7 * 24 * time.Hour

func (s *Service) warmRemoteImagesForStoredMessage(msg store.MessageRecord, mailbox store.Mailbox, bodyHTML string) {
	if s == nil || s.Store == nil || s.Blobs == nil || strings.TrimSpace(bodyHTML) == "" {
		return
	}
	if !shouldWarmRemoteImagesForMailbox(mailbox, msg.Date) {
		return
	}
	cache := remoteimages.Cache{
		Store: s.Store,
		Blobs: s.Blobs,
		Allow: func(ctx context.Context, req plugins.RemoteImageFetchRequest) (plugins.RemoteImageFetchDecision, error) {
			return s.allowRemoteImageFetch(ctx, req)
		},
	}
	cache.WarmMessageAsync(plugins.RemoteImageFetchRequest{
		UserID:    msg.UserID,
		MessageID: msg.MessageIDHeader,
		Mailbox:   mailbox.Name,
		Sender:    msg.FromAddr,
	}, bodyHTML)
}

func shouldWarmRemoteImagesForMailbox(mailbox store.Mailbox, date time.Time) bool {
	name := strings.ToLower(strings.TrimSpace(mailbox.Name))
	role := strings.ToLower(strings.TrimSpace(mailbox.Role))
	if role != "inbox" && name != "inbox" {
		return false
	}
	if date.IsZero() {
		return true
	}
	return date.After(time.Now().Add(-remoteImageWarmRecentWindow))
}

func (s *Service) allowRemoteImageFetch(ctx context.Context, req plugins.RemoteImageFetchRequest) (plugins.RemoteImageFetchDecision, error) {
	if s == nil || s.Store == nil || !s.pluginEnabled(ctx, plugins.RemoteImageBlocklist) {
		return plugins.RemoteImageFetchDecision{Allow: true}, nil
	}
	hook, ok := remoteImageFetchHook()
	if !ok {
		return plugins.RemoteImageFetchDecision{Allow: true}, nil
	}
	return hook.AllowRemoteImageFetch(ctx, s.Store.DB(), req)
}

func remoteImageFetchHook() (plugins.RemoteImageBlocklistHook, bool) {
	for _, hook := range plugins.Hooks(plugins.RemoteImageBlocklist) {
		typed, ok := hook.(plugins.RemoteImageBlocklistHook)
		if ok {
			return typed, true
		}
	}
	return nil, false
}

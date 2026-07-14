// File overview: Tenant-scoped message annotations for mail copied by remote IMAP sync.

package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

const annotationMessageBatchSize = 200

// MessageAnnotations maps durable provenance rows back to local destination
// messages. Provenance, rather than a raw RFC822 header, is used for the UI
// attribution because message headers can be supplied by senders.
func (*remoteIMAPSyncBackend) MessageAnnotations(ctx context.Context, host plugins.BackendHost, userID int64, messageIDs []int64) (map[int64][]plugins.MessageAnnotation, error) {
	out := map[int64][]plugins.MessageAnnotation{}
	if userID <= 0 || len(messageIDs) == 0 {
		return out, nil
	}
	if host == nil {
		return nil, errors.New("remote IMAP sync annotation host is not available")
	}
	st, ok := host.Store().(*store.Store)
	if !ok || st == nil {
		return nil, errors.New("remote IMAP sync annotation store is not available")
	}
	db, err := st.UserDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	return remoteIMAPSyncAnnotations(ctx, db, userID, messageIDs)
}

func remoteIMAPSyncAnnotations(ctx context.Context, db *sql.DB, userID int64, messageIDs []int64) (map[int64][]plugins.MessageAnnotation, error) {
	out := map[int64][]plugins.MessageAnnotation{}
	if db == nil || userID <= 0 {
		return out, nil
	}
	ids := uniquePositiveMessageIDs(messageIDs)
	for start := 0; start < len(ids); start += annotationMessageBatchSize {
		end := start + annotationMessageBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		args := make([]any, 0, len(chunk)+4)
		args = append(args, userID, userID, userID, userID)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx, `SELECT local_message.id, provenance.synced_at
			FROM messages AS local_message
			JOIN mailboxes AS mailbox
			  ON mailbox.user_id = local_message.user_id
			 AND mailbox.account_id = local_message.account_id
			 AND mailbox.id = local_message.mailbox_id
			JOIN blobs AS destination_blob
			  ON destination_blob.user_id = local_message.user_id
			 AND destination_blob.id = local_message.blob_id
			JOIN plugin_remote_imap_sync_provenance AS provenance
			  ON provenance.user_id = local_message.user_id
			 AND provenance.destination_account_id = local_message.account_id
			 AND provenance.destination_mailbox_id = local_message.mailbox_id
			 AND provenance.destination_uidvalidity = mailbox.uidvalidity
			 AND provenance.destination_uid = local_message.uid
			 AND provenance.destination_sha256 = destination_blob.sha256
			WHERE local_message.user_id = ?
			  AND mailbox.user_id = ?
			  AND destination_blob.user_id = ?
			  AND provenance.user_id = ?
			  AND local_message.id IN (`+annotationPlaceholders(len(chunk))+`)
			ORDER BY local_message.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var messageID, copiedAt int64
			if err := rows.Scan(&messageID, &copiedAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if copiedAt <= 0 || len(out[messageID]) != 0 {
				continue
			}
			metadata := map[string]string{
				"synced_at": time.Unix(copiedAt, 0).UTC().Format(time.RFC3339),
			}
			out[messageID] = []plugins.MessageAnnotation{{
				PluginID: pluginID,
				Kind:     "remote-imap-sync",
				Label:    "Synced by Rolltop",
				Level:    "info",
				Summary:  "Copied into this mailbox by Remote IMAP Sync.",
				Metadata: metadata,
			}}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func uniquePositiveMessageIDs(messageIDs []int64) []int64 {
	ids := make([]int64, 0, len(messageIDs))
	seen := make(map[int64]bool, len(messageIDs))
	for _, id := range messageIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func annotationPlaceholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

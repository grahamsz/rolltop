// File overview: Process ownership helpers for at-most-once remote transfer dispatch.

package syncer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"rolltop/backend/store"
)

var processMessageTransferOwner = newMessageTransferOwner()

func newMessageTransferOwner() string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		return "process-" + hex.EncodeToString(token[:])
	}
	return fmt.Sprintf("process-%d-%d", os.Getpid(), time.Now().UnixNano())
}

func messageTransferCanReconcile(transfer store.MessageTransfer) bool {
	if transfer.DispatchedAt.IsZero() {
		return false
	}
	return !transfer.DispatchFinishedAt.IsZero() || transfer.DispatchOwner != processMessageTransferOwner
}

func messageTransferClaim(transfer store.MessageTransfer) store.MessageTransferDispatchClaim {
	return store.MessageTransferDispatchClaim{Owner: transfer.DispatchOwner, Attempt: transfer.DispatchAttempt}
}

// File overview: Store-level error helpers.

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrDuplicateMailboxRole = errors.New("mailbox role already assigned")
var ErrInvalidMailboxSettings = errors.New("invalid mailbox settings")

// IsNotFound normalizes sql.ErrNoRows checks across store and web packages.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// WrapNotFound converts sql.ErrNoRows to the store package sentinel used by callers.
func WrapNotFound(thing string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", thing, ErrNotFound)
	}
	return err
}

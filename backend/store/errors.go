// File overview: Store-level error helpers.

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func WrapNotFound(thing string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", thing, ErrNotFound)
	}
	return err
}

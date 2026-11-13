// Work package B: see the collation design.

package ops

import (
	"context"

	"github.com/decisyon/dialga/engine/internal/validate"
)

// validateSetCollation is Validate for set-collation, after what the
// collation operations share (validateCollation).
func validateSetCollation(p Params) error {
	if err := validate.DBName(p.Database); err != nil {
		return err
	}
	return errNotYet
}

// SetCollation prepares the change of the default collation of a database
// (collation design §6.1).
func (s *Session) SetCollation(ctx context.Context, in Params) (*Prepared, error) {
	return nil, errNotYet
}

// runSetCollation runs the step of a set-collation (collation design §6.1).
func (s *Session) runSetCollation(ctx context.Context, st CollationStep) (string, error) {
	return "", errNotYet
}

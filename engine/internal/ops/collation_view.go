// Work package A: see the collation design.

package ops

import (
	"context"

	"github.com/decisyon/dialga/engine/internal/mssql"
)

// CollationOverview reads the collation view of the target: the server,
// tempdb and every user database (read-only).
func (s *Session) CollationOverview(ctx context.Context) (CollationOverview, error) {
	return CollationOverview{}, errNotYet
}

// DatabaseCollation reads the collation view of db (read-only):
// mssql.ErrNoDatabase when it does not exist, ErrNotOnline when it is not
// online.
func (s *Session) DatabaseCollation(ctx context.Context, db string) (DatabaseCollationView, error) {
	return DatabaseCollationView{}, errNotYet
}

// Collations lists the collations of the instance (read-only).
func (s *Session) Collations(ctx context.Context) ([]mssql.CollationInfo, error) {
	return nil, errNotYet
}

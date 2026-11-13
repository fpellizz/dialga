// Work package A: see the collation design.

package mssql

import "context"

// DatabaseCollations lists the user databases with their default collation,
// state and containment (collation design §4.1).
func (s Session) DatabaseCollations(ctx context.Context) ([]DatabaseCollation, error) {
	return nil, errNotYet
}

// Collations lists every collation of sys.fn_helpcollations, by name, with
// its traits (collation design §4.1).
func (s Session) Collations(ctx context.Context) ([]CollationInfo, error) {
	return nil, errNotYet
}

// CharColumns lists the columns of the user tables of db that have a
// collation (collation design §4.1).
func (s Session) CharColumns(ctx context.Context, db string) ([]CharColumn, error) {
	return nil, errNotYet
}

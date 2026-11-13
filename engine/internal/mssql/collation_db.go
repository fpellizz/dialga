// Work package B: see the collation design.

package mssql

import "context"

// CollationBlocker is an object that keeps ALTER DATABASE ... COLLATE from
// running because it depends on the collation of the database.
type CollationBlocker struct {
	// Kind is schema-bound, computed-column, check-constraint,
	// table-function or memory-optimized.
	Kind   string `json:"kind"`
	Schema string `json:"schema"`
	Object string `json:"object"`
	Detail string `json:"detail,omitempty"`
}

// CollationBlockers lists the objects of db that depend on its collation
// (collation design §4.2).
func (s Session) CollationBlockers(ctx context.Context, db string) ([]CollationBlocker, error) {
	return nil, errNotYet
}

// NameClashes lists (up to 20) the names of db that would collide under
// collation (collation design §4.2).
func (s Session) NameClashes(ctx context.Context, db, collation string) ([]string, error) {
	return nil, errNotYet
}

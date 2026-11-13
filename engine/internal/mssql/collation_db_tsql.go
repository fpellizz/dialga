// Work package B: see the collation design.

package mssql

// SetDatabaseCollation is the batch that changes the default collation of db
// to collation, in SINGLE_USER (collation design §5.1). Both names are
// validated by the caller.
func SetDatabaseCollation(db, collation string) string {
	return ""
}

// MultiUserIfSingle opens db to every session again if it was left in
// SINGLE_USER (collation design §5.1).
func MultiUserIfSingle(db string) string {
	return ""
}

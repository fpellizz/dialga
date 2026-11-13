package mssql

// SetDatabaseCollation is the batch that changes the default collation of db
// to collation, in SINGLE_USER (collation design §5.1). Both names are
// validated by the caller. A failure reopens the database to every session
// and rethrows.
func SetDatabaseCollation(db, collation string) string {
	d := quoteIdent(db)
	return "USE [master];\n" +
		"ALTER DATABASE " + d + " SET SINGLE_USER WITH ROLLBACK IMMEDIATE;\n" +
		"BEGIN TRY\n" +
		"    ALTER DATABASE " + d + " COLLATE " + collation + ";\n" +
		"END TRY\n" +
		"BEGIN CATCH\n" +
		"    ALTER DATABASE " + d + " SET MULTI_USER;\n" +
		"    THROW;\n" +
		"END CATCH;\n" +
		"ALTER DATABASE " + d + " SET MULTI_USER;"
}

// MultiUserIfSingle opens db to every session again if it was left in
// SINGLE_USER (collation design §5.1).
func MultiUserIfSingle(db string) string {
	return "IF EXISTS (SELECT 1 FROM sys.databases WHERE name = " + Str(db) + " AND user_access_desc = N'SINGLE_USER')\n" +
		"    ALTER DATABASE " + quoteIdent(db) + " SET MULTI_USER;"
}

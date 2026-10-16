package mssql

import (
	"strconv"
	"strings"
)

// The statements below are the ones mssql-import.sh runs, character for
// character (testdata/golden). They only format: callers validate names and
// keys first (package validate), and Str still escapes every literal.

// Str quotes s as a T-SQL Unicode literal, doubling single quotes.
func Str(s string) string { return "N'" + strings.ReplaceAll(s, "'", "''") + "'" }

// S3ARN is the ARN of an object, as the rds_* procedures expect it.
func S3ARN(bucket, key string) string { return "arn:aws:s3:::" + bucket + "/" + key }

// RestoreOptions are the variants of rds_restore_database.
type RestoreOptions struct {
	// Differential restores a differential backup on a database left in
	// RESTORING by a full restore with NoRecovery.
	Differential bool
	// NoRecovery leaves the database in RESTORING (rds_finish_restore ends it).
	NoRecovery bool
}

// RestoreDatabase starts a native restore of the .bak at s3ARN into db.
func RestoreDatabase(db, s3ARN string, o RestoreOptions) string {
	var b strings.Builder
	b.WriteString("exec msdb.dbo.rds_restore_database\n")
	b.WriteString("     @restore_db_name = " + Str(db) + ",\n")
	b.WriteString("     @s3_arn_to_restore_from = " + Str(s3ARN))
	if o.Differential {
		b.WriteString(",\n     @type = N'DIFFERENTIAL'")
	}
	if o.NoRecovery {
		b.WriteString(",\n     @with_norecovery = 1")
	}
	b.WriteString(";")
	return b.String()
}

// BackupDatabase starts a native backup of db to s3ARN, never overwriting.
func BackupDatabase(db, s3ARN string) string {
	return "exec msdb.dbo.rds_backup_database\n" +
		"     @source_db_name = " + Str(db) + ",\n" +
		"     @s3_arn_to_backup_to = " + Str(s3ARN) + ",\n" +
		"     @overwrite_s3_backup_file = 0;"
}

// FinishRestore brings a database restored with NoRecovery online.
func FinishRestore(db string) string {
	return "exec msdb.dbo.rds_finish_restore @db_name = " + Str(db) + ";"
}

// DropDatabase drops db the RDS way (it also clears its backup history).
func DropDatabase(db string) string {
	return "exec msdb.dbo.rds_drop_database @db_name = " + Str(db) + ";"
}

// CancelTask asks RDS to cancel a native task.
func CancelTask(id int) string {
	return "exec msdb.dbo.rds_cancel_task @task_id = " + strconv.Itoa(id) + ";"
}

// SetSingleUser closes the database to every session but one, rolling back
// what the others are doing: the state RDS wants before a rename.
func SetSingleUser(db string) string {
	return "ALTER DATABASE " + quoteIdent(db) + " SET SINGLE_USER WITH ROLLBACK IMMEDIATE;"
}

// SetMultiUser opens the database to every session again.
func SetMultiUser(db string) string {
	return "ALTER DATABASE " + quoteIdent(db) + " SET MULTI_USER;"
}

// RenameDatabase renames a database the RDS way (the database must be in
// SINGLE_USER mode). TODO: confermare su mssql-dev.
func RenameDatabase(from, to string) string {
	return "EXEC rdsadmin.dbo.rds_modify_db_name " + Str(from) + ", " + Str(to) + ";"
}

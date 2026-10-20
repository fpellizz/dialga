-- Mock of the msdb.dbo.rds_* procedures for a plain SQL Server, used by the integration tests.
-- Derived from a mock kept outside this repository (the original bash scripts mssql-import.sh / mssql-bak-inspect.sh).
-- Load it into a throwaway SQL Server 2022 container, one batch per GO; the last batch needs a writable /bak directory.
USE msdb;
GO
CREATE TABLE dbo.mock_tasks(
  task_id int IDENTITY(1,1) PRIMARY KEY, task_type varchar(50), database_name sysname,
  pct int, lifecycle varchar(30), task_info nvarchar(max), last_updated datetime,
  created_at datetime, S3_object_arn nvarchar(1000));
GO
CREATE FUNCTION dbo.rds_fn_task_status(@db sysname, @id int) RETURNS TABLE AS RETURN
  SELECT task_id, task_type, database_name, pct AS [% complete], 0 AS [duration(mins)],
         lifecycle, task_info, last_updated, created_at, S3_object_arn,
         0 AS overwrite_S3_backup_file, CAST(NULL AS varchar(10)) AS KMS_master_key_arn,
         CAST(NULL AS varchar(10)) AS filepath, 0 AS overwrite_file, CAST(NULL AS varchar(10)) AS task_metadata
  FROM dbo.mock_tasks WHERE @id = 0 OR task_id = @id;
GO
CREATE PROCEDURE dbo.rds_backup_database @source_db_name sysname, @s3_arn_to_backup_to nvarchar(1000),
  @overwrite_s3_backup_file int = 0 AS
BEGIN
  INSERT dbo.mock_tasks(task_type, database_name, pct, lifecycle, task_info, last_updated, created_at, S3_object_arn)
  VALUES ('BACKUP_DB', @source_db_name, 0, 'CREATED',
          N'[2026-10-15 13:00:00.000] Task created.' + CHAR(13) + CHAR(10) + N'[2026-10-15 13:00:01.000] Waiting.',
          GETDATE(), GETDATE(), @s3_arn_to_backup_to);
  SELECT * FROM dbo.rds_fn_task_status(NULL, SCOPE_IDENTITY());
END
GO
CREATE PROCEDURE dbo.rds_restore_database @restore_db_name sysname, @s3_arn_to_restore_from nvarchar(1000),
  @with_norecovery int = 0, @type varchar(20) = 'FULL' AS
BEGIN
  INSERT dbo.mock_tasks(task_type, database_name, pct, lifecycle, task_info, last_updated, created_at, S3_object_arn)
  VALUES ('RESTORE_DB', @restore_db_name, 0, 'CREATED', N'restore created', GETDATE(), GETDATE(), @s3_arn_to_restore_from);
  SELECT * FROM dbo.rds_fn_task_status(NULL, SCOPE_IDENTITY());
END
GO
CREATE PROCEDURE dbo.rds_drop_database @db_name sysname AS
BEGIN
  DECLARE @s nvarchar(400) = N'ALTER DATABASE ' + QUOTENAME(@db_name) + N' SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ' + QUOTENAME(@db_name);
  EXEC (@s);
  PRINT N'mock: dropped ' + @db_name;
END
GO
CREATE PROCEDURE dbo.rds_cancel_task @task_id int AS
  UPDATE dbo.mock_tasks SET lifecycle = 'CANCELLED', last_updated = GETDATE() WHERE task_id = @task_id;
GO
-- rdsadmin.dbo.rds_modify_db_name, the RDS way to rename a database (it wants SINGLE_USER first).
-- TODO: confermare su dev-sql. Not in the original bash mock: added for the restore-over-existing tests.
CREATE DATABASE rdsadmin;
GO
USE rdsadmin;
GO
CREATE PROCEDURE dbo.rds_modify_db_name @old sysname, @new sysname AS
BEGIN
  DECLARE @s nvarchar(400) = N'ALTER DATABASE ' + QUOTENAME(@old) + N' MODIFY NAME = ' + QUOTENAME(@new);
  EXEC (@s);
END
GO
-- A "admin" login like the RDS master user, and a DB with an orphaned user
USE master;
CREATE LOGIN admin WITH PASSWORD = 'Mock-admin-1!', CHECK_POLICY = OFF;
ALTER SERVER ROLE dbcreator ADD MEMBER admin;
CREATE LOGIN appuser WITH PASSWORD = 'Mock-app-1!', CHECK_POLICY = OFF;
GO
CREATE DATABASE app_db;
GO
USE app_db;
CREATE USER admin FOR LOGIN admin; ALTER ROLE db_owner ADD MEMBER admin;
CREATE USER appuser FOR LOGIN appuser;
CREATE TABLE dbo.t1(id int PRIMARY KEY, v nvarchar(50));
INSERT dbo.t1 VALUES (1, N'a'), (2, N'b');
GO
CREATE VIEW dbo.v1 AS SELECT id FROM dbo.t1;
GO
USE master;
DROP LOGIN appuser;   -- appuser is now orphaned in app_db
GO
USE msdb;
GRANT SELECT ON dbo.rds_fn_task_status TO public;
CREATE USER admin FOR LOGIN admin;
GRANT EXECUTE ON dbo.rds_backup_database TO admin; GRANT EXECUTE ON dbo.rds_restore_database TO admin;
GRANT EXECUTE ON dbo.rds_drop_database TO admin; GRANT EXECUTE ON dbo.rds_cancel_task TO admin;
GO
USE rdsadmin;
CREATE USER admin FOR LOGIN admin;
GRANT EXECUTE ON dbo.rds_modify_db_name TO admin;
GO
-- the test .bak for the inspect script (2022 -> version 16, must be KO)
BACKUP DATABASE app_db TO DISK = N'/bak/app_db_2022.bak' WITH COMPRESSION, CHECKSUM, INIT;
GO

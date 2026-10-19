USE [msdb];
REVOKE EXECUTE ON [dbo].[rds_backup_database] FROM [golden_user];
REVOKE EXECUTE ON [dbo].[rds_task_status] FROM [golden_user];
REVOKE SELECT ON [dbo].[rds_fn_task_status] FROM [golden_user];

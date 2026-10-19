USE [msdb];
GRANT EXECUTE ON [dbo].[rds_backup_database] TO [golden_user];
GRANT EXECUTE ON [dbo].[rds_task_status] TO [golden_user];
GRANT SELECT ON [dbo].[rds_fn_task_status] TO [golden_user];

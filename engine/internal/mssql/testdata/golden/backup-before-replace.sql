exec msdb.dbo.rds_backup_database
     @source_db_name = N'golden_db',
     @s3_arn_to_backup_to = N'arn:aws:s3:::golden-bucket/export/golden_db_before_replace_20261016091500.bak',
     @overwrite_s3_backup_file = 0;

exec msdb.dbo.rds_restore_database
     @restore_db_name = N'golden_db',
     @s3_arn_to_restore_from = N'arn:aws:s3:::golden-bucket/import/golden_diff.bak',
     @type = N'DIFFERENTIAL';

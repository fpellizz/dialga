# Minimal IAM policy

`iam-policy.json` grants a Dialga user what the engine calls, and nothing
else. Attach it to the IAM user, group or role behind the `aws_profile` of
each target (one copy per account and region if targets live in several).

## Placeholders

| Placeholder | Replace with | Example |
|---|---|---|
| `[account-id]` | `account_id` of the target | `123456789012` |
| `[region]` | `region` of the target | `eu-west-1` |
| `[bucket]` | `bucket` of the target | `my-sqlserver-backups` |
| `[import-prefix]` | `prefixes.import` (default `import`) | `import` |
| `[export-prefix]` | `prefixes.export` (default `export`) | `export` |
| `[secret-name]` | Only for the optional statement, see below | `dialga/dev-sql` |

Several targets on the same bucket: add one `[bucket]/[prefix]/*` line per
prefix. Several buckets: one `BackupBucket` resource and the object
resources for each.

## Statements

| Sid | Actions | Used by |
|---|---|---|
| `Identity` | `sts:GetCallerIdentity` | Account guard on every command |
| `RdsDescribe` | `rds:DescribeDBInstances`, `rds:DescribeOptionGroups` | Check: instance state, endpoint, option group, storage |
| `RdsFreeStorage` | `cloudwatch:GetMetricStatistics` | Check: free storage (`FreeStorageSpace`). No resource-level permissions in CloudWatch: `*` |
| `BackupBucket` | `s3:ListBucket` | Check: `HeadBucket`. Not limited by prefix, or `HeadBucket` is denied |
| `BackupObjects` | `s3:GetObject`, `s3:PutObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` | Upload of `.bak` files to the import prefix, download from the export prefix, resume and cancel of multipart uploads |
| `TunnelFindBridge` | `ec2:DescribeInstances` | Tunnel mode: find the SSM bridge. No resource-level permissions: `*` |
| `TunnelStartSession` | `ssm:StartSession` | Tunnel mode: port forwarding through the bridge, with the `AWS-StartPortForwardingSessionToRemoteHost` document only (no shell) |
| `TunnelTerminateOwnSession` | `ssm:TerminateSession` | Tunnel mode: close the user's own sessions only |
| `OptionalSqlPasswordFromSecretsManager` | `secretsmanager:GetSecretValue` | Only with `sql.password: secretsmanager:<ARN>` (planned). Remove it otherwise |

## What the policy does not cover

- **RDS reading and writing S3**: `rds_restore_database` and
  `rds_backup_database` use the IAM role of the `SQLSERVER_BACKUP_RESTORE`
  option in the option group, not the user's credentials. That role is set
  up once per instance, as described in the AWS documentation on native
  backup and restore for RDS for SQL Server.
- **SQL Server**: the login comes from the target configuration (`sql.user`
  and the password reference), not from IAM.
- **KMS**: with SSE-KMS on the bucket, add `kms:GenerateDataKey` and
  `kms:Decrypt` on that key, for the user and for the option role.
- **Tunnel scope**: `ssm:StartSession` is allowed on every instance of the
  account and region. To limit it to the bridge, add a condition on its tag,
  for example `"Condition": {"StringEquals": {"ssm:resourceTag/Name": "ssm-bridge"}}`
  on the instance resource (split the statement: the condition does not
  apply to the document).

# Policy IAM minima

`iam-policy.json` concede a un utente di Dialga ciò che l'engine chiama, e
nient'altro. Va associata all'utente, al gruppo o al ruolo IAM dietro
l'`aws_profile` di ogni target (una copia per account e regione, se i target
stanno in più account o regioni).

## Segnaposto

| Segnaposto | Da sostituire con | Esempio |
|---|---|---|
| `[account-id]` | `account_id` del target | `123456789012` |
| `[region]` | `region` del target | `eu-west-1` |
| `[bucket]` | `bucket` del target | `my-sqlserver-backups` |
| `[import-prefix]` | `prefixes.import` (default `import`) | `import` |
| `[export-prefix]` | `prefixes.export` (default `export`) | `export` |
| `[secret-name]` | Solo per lo statement facoltativo, vedi sotto | `dialga/dev-sql` |

Più target sullo stesso bucket: aggiungi una riga `[bucket]/[prefix]/*` per
ogni prefisso. Più bucket: una risorsa `BackupBucket` e le risorse degli
oggetti per ciascuno.

## Statement

| Sid | Azioni | A cosa servono |
|---|---|---|
| `Identity` | `sts:GetCallerIdentity` | Controllo dell'account su ogni comando |
| `RdsDescribe` | `rds:DescribeDBInstances`, `rds:DescribeOptionGroups` | Check: stato dell'istanza, endpoint, option group, storage |
| `RdsFreeStorage` | `cloudwatch:GetMetricStatistics` | Check: spazio libero (`FreeStorageSpace`). CloudWatch non ha permessi a livello di risorsa: `*` |
| `BackupBucket` | `s3:ListBucket` | Check: `HeadBucket`. Non limitato per prefisso, altrimenti `HeadBucket` viene negato |
| `BackupObjects` | `s3:GetObject`, `s3:PutObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` | Upload dei `.bak` nel prefisso di import, download dal prefisso di export, ripresa e annullamento degli upload multipart |
| `TunnelFindBridge` | `ec2:DescribeInstances` | Modalità tunnel: trovare il bridge SSM. Nessun permesso a livello di risorsa: `*` |
| `TunnelStartSession` | `ssm:StartSession` | Modalità tunnel: port forwarding attraverso il bridge, solo con il documento `AWS-StartPortForwardingSessionToRemoteHost` (nessuna shell) |
| `TunnelTerminateOwnSession` | `ssm:TerminateSession` | Modalità tunnel: chiudere solo le sessioni dell'utente stesso |
| `OptionalSqlPasswordFromSecretsManager` | `secretsmanager:GetSecretValue` | Solo con `sql.password: secretsmanager:<ARN>` (previsto). Altrimenti va tolto |

## Cosa la policy non copre

- **RDS che legge e scrive su S3**: `rds_restore_database` e
  `rds_backup_database` usano il ruolo IAM dell'opzione
  `SQLSERVER_BACKUP_RESTORE` dell'option group, non le credenziali
  dell'utente. Quel ruolo si configura una volta per istanza, come descritto
  nella documentazione AWS sul backup e restore nativo di RDS for SQL
  Server.
- **SQL Server**: il login viene dalla configurazione del target
  (`sql.user` e il riferimento alla password), non da IAM.
- **KMS**: con SSE-KMS sul bucket, aggiungi `kms:GenerateDataKey` e
  `kms:Decrypt` su quella chiave, per l'utente e per il ruolo dell'opzione.
- **Ambito del tunnel**: `ssm:StartSession` è consentito su tutte le istanze
  dell'account e della regione. Per limitarlo al bridge aggiungi una
  condizione sul suo tag, per esempio
  `"Condition": {"StringEquals": {"ssm:resourceTag/Name": "ssm-bridge"}}`
  sulla risorsa dell'istanza (dividi lo statement: la condizione non si
  applica al documento).

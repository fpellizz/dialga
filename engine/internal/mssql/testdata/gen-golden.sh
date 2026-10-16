#!/usr/bin/env bash
# Golden T-SQL for Dialga, produced by mssql-import.sh itself:
# finish/drop/cancel from its dry-run output, restore/backup from its
# templates (cmd_restore, cmd_export) with its own tsql_str.
set -euo pipefail
src=${MSSQL_IMPORT:?set MSSQL_IMPORT to the path of mssql-import.sh}
out=${1:?usage: gen-golden.sh <output dir>}
dry() { "$src" "$@" 2>&1 | sed -n '2,$p' | sed 's/^      //'; }
# single-user, multi-user, rename and backup-before-replace .sql are written by hand
dry finish golden_db > "$out/finish.sql"
dry drop golden_db > "$out/drop.sql"
dry cancel 42 > "$out/cancel.sql"
eval "$(grep '^tsql_str()' "$src")"
BUCKET=golden-bucket
restore() { # db key differential norecovery — same text as cmd_restore
  local db=$1 key=$2 DIFFERENTIAL=$3 NORECOVERY=$4 tsql
  tsql="exec msdb.dbo.rds_restore_database
     @restore_db_name = $(tsql_str "$db"),
     @s3_arn_to_restore_from = $(tsql_str "arn:aws:s3:::$BUCKET/$key")"
  ((DIFFERENTIAL)) && tsql+=",
     @type = N'DIFFERENTIAL'"
  ((NORECOVERY)) && tsql+=",
     @with_norecovery = 1"
  tsql+=";"
  printf '%s\n' "$tsql"
}
backup() { # db key — same text as cmd_export
  local db=$1 key=$2 tsql
  tsql="exec msdb.dbo.rds_backup_database
     @source_db_name = $(tsql_str "$db"),
     @s3_arn_to_backup_to = $(tsql_str "arn:aws:s3:::$BUCKET/$key"),
     @overwrite_s3_backup_file = 0;"
  printf '%s\n' "$tsql"
}
restore golden_db import/golden.bak 0 0 > "$out/restore.sql"
restore golden_db import/golden_full.bak 0 1 > "$out/restore-norecovery.sql"
restore golden_db import/golden_diff.bak 1 0 > "$out/restore-differential.sql"
backup golden_db "export/golden_db_20261016-000000.bak" > "$out/backup.sql"
backup "o-neil_db" "export/it's here.bak" > "$out/backup-quote.sql"

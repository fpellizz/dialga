import type { DatabaseReport, PlanPreview } from "../plans";

/** Plans as the engine previews them on the placeholder targets of fixtures.ts. */
export const DROP_PREVIEW: PlanPreview = {
  plan: {
    operation: "drop",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    subject: "sales",
    actions: [
      {
        id: "drop",
        title: "Drop database sales",
        kind: "sql",
        risk: "destructive",
        statement: "exec msdb.dbo.rds_drop_database @db_name = N'sales';",
      },
    ],
  },
  checks: [
    { step: "database", label: "Database", status: "ok", detail: "sales is ONLINE" },
    {
      step: "tasks",
      label: "Native tasks",
      status: "ok",
      detail: "no native task of sales running",
    },
  ],
  ready: true,
  confirmation: "sales",
  digest: "d".repeat(32),
  preview: "Dry run: drop on dev-sql (instance my-sqlserver). Nothing is changed.",
  request: { database: "sales" },
};

/** On a protected target any change asks for the target name. */
export const PROTECTED_DROP: PlanPreview = {
  ...DROP_PREVIEW,
  plan: {
    ...DROP_PREVIEW.plan,
    target: "prod-sql",
    instance: "my-sqlserver-prod",
    protected: true,
  },
  confirmation: "prod-sql",
};

export const RESTORE_PREVIEW: PlanPreview = {
  plan: {
    operation: "restore",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    actions: [
      {
        id: "restore",
        title: "Restore sales_copy from s3://my-sqlserver-backups/import/sales.bak",
        kind: "sql",
        risk: "change",
        statement:
          "exec msdb.dbo.rds_restore_database\n     @restore_db_name = N'sales_copy',\n     @s3_arn_to_restore_from = N'arn:aws:s3:::my-sqlserver-backups/import/sales.bak';",
      },
    ],
  },
  checks: [
    {
      step: "source",
      label: "Backup in S3",
      status: "ok",
      detail: "s3://my-sqlserver-backups/import/sales.bak (3.0 MiB)",
    },
    {
      step: "database",
      label: "Target database",
      status: "ok",
      detail: "sales_copy does not exist yet",
    },
    {
      step: "tasks",
      label: "Native tasks",
      status: "warning",
      detail: "1 native task(s) running or queued: RDS runs two at a time, the new one may wait",
    },
  ],
  ready: true,
  digest: "r".repeat(32),
  preview: "Dry run: restore on dev-sql",
  request: { database: "sales_copy", key: "import/sales.bak" },
};

export const EXPORT_PREVIEW: PlanPreview = {
  plan: {
    operation: "export",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    actions: [
      {
        id: "export",
        title: "Back up sales to s3://my-sqlserver-backups/export/sales_20261016-083000.bak",
        kind: "sql",
        risk: "change",
        statement:
          "exec msdb.dbo.rds_backup_database\n     @source_db_name = N'sales',\n     @s3_arn_to_backup_to = N'arn:aws:s3:::my-sqlserver-backups/export/sales_20261016-083000.bak',\n     @overwrite_s3_backup_file = 0;",
      },
    ],
  },
  checks: [
    { step: "database", label: "Database", status: "ok", detail: "sales is ONLINE" },
    {
      step: "destination",
      label: "Destination in S3",
      status: "ok",
      detail: "s3://my-sqlserver-backups/export/sales_20261016-083000.bak is free",
    },
    { step: "tasks", label: "Native tasks", status: "ok", detail: "no native task running" },
  ],
  ready: true,
  digest: "e".repeat(32),
  preview: "Dry run: export on dev-sql",
  request: { database: "sales", key: "export/sales_20261016-083000.bak" },
};

export const FINISH_NOT_READY: PlanPreview = {
  plan: {
    operation: "finish",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    actions: [
      {
        id: "finish",
        title: "Finish the restore of sales (bring it online)",
        kind: "sql",
        risk: "change",
        statement: "exec msdb.dbo.rds_finish_restore @db_name = N'sales';",
      },
    ],
  },
  checks: [
    {
      step: "database",
      label: "Database",
      status: "failed",
      detail: "sales is ONLINE, not RESTORING: nothing to finish",
    },
  ],
  ready: false,
  digest: "f".repeat(32),
  preview: "Dry run: finish on dev-sql",
  request: { database: "sales" },
};

export const CANCEL_PREVIEW: PlanPreview = {
  plan: {
    operation: "cancel",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    actions: [
      {
        id: "cancel",
        title: "Cancel task 7 (RESTORE_DB of sales)",
        kind: "sql",
        risk: "change",
        statement: "exec msdb.dbo.rds_cancel_task @task_id = 7;",
      },
    ],
  },
  checks: [
    {
      step: "task",
      label: "Native task",
      status: "ok",
      detail: "task 7 (RESTORE_DB of sales): IN_PROGRESS 40%",
    },
  ],
  ready: true,
  digest: "c".repeat(32),
  preview: "Dry run: cancel on dev-sql",
  request: { taskId: 7 },
};

export const REPORT: DatabaseReport = {
  target: "dev-sql",
  instance: "my-sqlserver",
  ok: true,
  findings: [
    {
      step: "database",
      label: "Database",
      status: "ok",
      detail: "ONLINE, compatibility level 140",
    },
    { step: "access", label: "Login access", status: "ok", detail: "admin can enter sales" },
    {
      step: "orphans",
      label: "Orphaned users",
      status: "warning",
      detail: "1 SQL user(s) without a login on the instance: app",
    },
    {
      step: "windows",
      label: "Windows users",
      status: "warning",
      detail: "2 Windows user(s) or group(s): unusable on RDS without Active Directory",
    },
  ],
  database: {
    name: "sales",
    state: "ONLINE",
    recoveryModel: "FULL",
    compatibilityLevel: 140,
    collation: "SQL_Latin1_General_CP1_CI_AS",
    owner: "rdsa",
    userAccess: "MULTI_USER",
    readOnly: false,
  },
  files: [
    { type: "ROWS", name: "sales", sizeMB: 8, physicalName: "D:\\rdsdbdata\\DATA\\sales.mdf" },
    {
      type: "LOG",
      name: "sales_log",
      sizeMB: 1.5,
      maxSizeMB: 2048,
      physicalName: "D:\\rdsdbdata\\DATA\\sales_log.ldf",
    },
  ],
  access: true,
  orphanUsers: ["app"],
  windowsPrincipals: 2,
  objects: [
    { type: "USER_TABLE", count: 12 },
    { type: "VIEW", count: 3 },
  ],
};

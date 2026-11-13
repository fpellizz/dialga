import type { CollationInfo, CollationOverview, DatabaseCollationView } from "../collation";
import type { PlanPreview } from "../plans";

/** Placeholders only: a few of the collations of an instance. */
export function info(name: string, extra: Partial<CollationInfo> = {}): CollationInfo {
  return {
    name,
    description: name,
    codePage: 1252,
    version: 0,
    caseSensitive: false,
    accentSensitive: true,
    kanaSensitive: false,
    widthSensitive: false,
    binary: false,
    utf8: false,
    legacy: false,
    ...extra,
  };
}

export const COLLATIONS: CollationInfo[] = [
  info("Cyrillic_General_CI_AS", { codePage: 1251 }),
  info("Latin1_General_100_CI_AS", { version: 2 }),
  info("Latin1_General_100_CI_AS_SC_UTF8", { version: 2, utf8: true }),
  info("Latin1_General_100_CS_AS", { version: 2, caseSensitive: true }),
  info("Latin1_General_BIN2", { binary: true }),
  info("SQL_Latin1_General_CP1_CI_AS", { legacy: true }),
];

export const COLLATION_OVERVIEW: CollationOverview = {
  target: "dev-sql",
  instance: "my-sqlserver",
  server: "SQL_Latin1_General_CP1_CI_AS",
  tempdb: "SQL_Latin1_General_CP1_CI_AS",
  serverFixed: true,
  databases: [
    {
      name: "sales",
      state: "ONLINE",
      collation: "Latin1_General_100_CI_AS",
      containment: "NONE",
      mismatchServer: true,
      tempTableRisk: true,
    },
    {
      name: "hr",
      state: "ONLINE",
      collation: "SQL_Latin1_General_CP1_CI_AS",
      containment: "NONE",
      mismatchServer: false,
      tempTableRisk: false,
    },
  ],
};

export const COLLATION_VIEW: DatabaseCollationView = {
  target: "dev-sql",
  instance: "my-sqlserver",
  database: "sales",
  collation: "Latin1_General_100_CI_AS",
  server: "SQL_Latin1_General_CP1_CI_AS",
  tempdb: "SQL_Latin1_General_CP1_CI_AS",
  containment: "NONE",
  mismatchServer: true,
  tempTableRisk: true,
  charColumns: 6,
  groups: [
    { collation: "Latin1_General_100_CI_AS", default: true, columns: 2, tables: 1, codePage: 1252 },
    {
      collation: "SQL_Latin1_General_CP1_CI_AS",
      default: false,
      columns: 4,
      tables: 3,
      codePage: 1252,
    },
  ],
  columns: [
    {
      schema: "dbo",
      table: "customers",
      column: "name",
      type: "nvarchar",
      baseType: "nvarchar",
      maxLength: 100,
      nullable: false,
      collation: "SQL_Latin1_General_CP1_CI_AS",
    },
    {
      schema: "dbo",
      table: "customers",
      column: "city",
      type: "varchar",
      baseType: "varchar",
      maxLength: 50,
      nullable: true,
      collation: "SQL_Latin1_General_CP1_CI_AS",
    },
    {
      schema: "sales",
      table: "orders",
      column: "note",
      type: "ntext",
      baseType: "ntext",
      maxLength: 16,
      nullable: true,
      collation: "SQL_Latin1_General_CP1_CI_AS",
      unsupported: "text/ntext: change it to varchar(max)/nvarchar(max) first",
    },
    {
      schema: "sales",
      table: "refunds",
      column: "reason",
      type: "nvarchar",
      baseType: "nvarchar",
      maxLength: 200,
      nullable: true,
      collation: "SQL_Latin1_General_CP1_CI_AS",
    },
  ],
  truncated: false,
};

export const SET_COLLATION_PREVIEW: PlanPreview = {
  plan: {
    operation: "set-collation",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    subject: "sales",
    actions: [
      {
        id: "set-collation",
        title: "Change the default collation of sales from A to B (disconnects its sessions)",
        kind: "sql",
        risk: "destructive",
        statement: "ALTER DATABASE [sales] COLLATE Latin1_General_100_CS_AS;",
      },
    ],
  },
  checks: [
    { step: "database", label: "Database", status: "ok", detail: "sales is ONLINE" },
    { step: "access", label: "Database access", status: "ok", detail: "MULTI_USER" },
    {
      step: "names",
      label: "Names under the new collation",
      status: "ok",
      detail: "no clashes",
    },
    {
      step: "columns",
      label: "Existing columns",
      status: "warning",
      detail: "4 column(s) keep Latin1_General_100_CI_AS: convert them with convert-columns",
    },
  ],
  ready: true,
  confirmation: "sales",
  digest: "s".repeat(32),
  preview: "Dry run: set-collation on dev-sql",
  request: { database: "sales", collation: "Latin1_General_100_CS_AS" },
  collation: {
    database: "sales",
    from: "Latin1_General_100_CI_AS",
    to: "Latin1_General_100_CS_AS",
    units: 0,
    tables: 0,
    columns: 0,
    indexes: 0,
    foreignKeys: 0,
    dataMB: 0,
  },
};

export const CONVERT_PREVIEW: PlanPreview = {
  plan: {
    operation: "convert-columns",
    target: "dev-sql",
    instance: "my-sqlserver",
    protected: false,
    subject: "sales",
    actions: [
      {
        id: "convert",
        title: "Convert 2 column(s) of [dbo].[customers] to Latin1_General_100_CI_AS",
        kind: "sql",
        risk: "destructive",
        statement:
          "USE [sales];\nALTER TABLE [dbo].[customers] ALTER COLUMN [name] nvarchar(100) COLLATE Latin1_General_100_CI_AS NOT NULL;",
      },
    ],
  },
  checks: [
    { step: "database", label: "Database", status: "ok", detail: "sales is ONLINE" },
    { step: "scope", label: "Scope", status: "ok", detail: "1 table, 2 columns, 1 unit" },
    {
      step: "refused",
      label: "Refused",
      status: "warning",
      detail: "[sales].[orders]: text/ntext",
    },
  ],
  ready: true,
  confirmation: "sales",
  digest: "c".repeat(32),
  preview: "Dry run: convert-columns on dev-sql",
  request: { database: "sales", collation: "Latin1_General_100_CI_AS" },
  collation: {
    database: "sales",
    to: "Latin1_General_100_CI_AS",
    units: 1,
    tables: 1,
    columns: 2,
    indexes: 3,
    foreignKeys: 1,
    dataMB: 12.5,
    refused: [
      { schema: "sales", name: "orders", reason: "text/ntext: change it to varchar(max) first" },
    ],
  },
};

import type { DatabaseAccess, Login } from "../access";
import type { PlanPreview, PlanRequest } from "../plans";

/** Placeholders only: the users and logins of the `sales` database on dev-sql. */
export const LOGINS: Login[] = [
  { name: "admin", disabled: false, master: true },
  { name: "app_rw", disabled: false, master: false },
  { name: "reporter", disabled: false, master: false },
];

export const ACCESS: DatabaseAccess = {
  target: "dev-sql",
  instance: "my-sqlserver",
  database: "sales",
  users: [
    {
      name: "dbo",
      login: "admin",
      orphan: false,
      roles: ["db_owner"],
      level: "owner",
      backup: false,
      managed: false,
    },
    {
      name: "app_rw",
      login: "app_rw",
      orphan: false,
      roles: ["db_datareader", "db_datawriter", "db_backupoperator"],
      level: "readwrite",
      backup: true,
      managed: true,
    },
    {
      name: "legacy",
      orphan: true,
      roles: ["db_datareader"],
      level: "read",
      backup: false,
      managed: true,
    },
  ],
  windowsPrincipals: 2,
};

/** A plan of an access operation, as the engine previews it on dev-sql. */
export function accessPreview(
  operation: string,
  request: PlanRequest,
  extra: Partial<PlanPreview> = {},
): PlanPreview {
  return {
    plan: {
      operation,
      target: "dev-sql",
      instance: "my-sqlserver",
      protected: false,
      subject: "sales",
      actions: [
        {
          id: "add-role",
          title: "Add app_rw to db_datareader",
          kind: "sql",
          risk: "change",
          statement: "ALTER ROLE [db_datareader] ADD MEMBER [app_rw];",
        },
        {
          id: "add-role",
          title: "Add app_rw to db_datawriter",
          kind: "sql",
          risk: "change",
          statement: "ALTER ROLE [db_datawriter] ADD MEMBER [app_rw];",
        },
      ],
    },
    checks: [{ step: "login", label: "Login", status: "ok", detail: "app_rw exists" }],
    ready: true,
    digest: "a".repeat(32),
    preview: `Dry run: ${operation} on dev-sql. Nothing is changed.`,
    request,
    ...extra,
  };
}

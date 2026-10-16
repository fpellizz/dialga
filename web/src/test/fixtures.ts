import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import type {
  Database,
  DiscoveredInstance,
  Overview,
  Prerequisite,
  TargetSummary,
  Task,
} from "../api";
import { I18nProvider, type Lang } from "../i18n";

/** Where the engine says the configuration lives (placeholder path). */
export const CONFIG_PATH = "/home/user/.config/dialga/config.yaml";

/** Placeholders only: no real account, instance or bucket. */
export const DEV: TargetSummary = {
  name: "dev-sql",
  instance: "my-sqlserver",
  region: "eu-west-1",
  profile: "default",
  accountId: "123456789012",
  bucket: "my-sqlserver-backups",
  sqlUser: "admin",
  connectMode: "external",
  localPort: 11433,
  protected: false,
};

export const PROD: TargetSummary = {
  ...DEV,
  name: "prod-sql",
  instance: "my-sqlserver-prod",
  profile: "prod",
  accountId: "210987654321",
  bucket: "my-sqlserver-prod-backups",
  connectMode: "direct",
  localPort: undefined,
  protected: true,
};

export const LAB: TargetSummary = {
  ...DEV,
  name: "lab-sql",
  instance: "my-sqlserver-lab",
  connectMode: "tunnel",
  localPort: undefined,
};

export const RESTORING: Task = {
  id: 7,
  type: "RESTORE_DB",
  database: "sales",
  percent: 40,
  lifecycle: "IN_PROGRESS",
  info: "Task execution has started.\r\nsales.bak: 40% processed.\r\n",
  createdAt: "2026-10-16T09:30:00",
  updatedAt: "2026-10-16T09:41:07",
};

export const RESTORED: Task = {
  ...RESTORING,
  percent: 100,
  lifecycle: "SUCCESS",
  updatedAt: "2026-10-16T09:52:30",
};

export const BACKUP_FAILED: Task = {
  id: 6,
  type: "BACKUP_DB",
  database: "hr",
  percent: 0,
  lifecycle: "ERROR",
  updatedAt: "2026-10-15T18:02:44",
};

/** GET /overview of a target: an available instance; SQL skipped (no password) by default. */
export function overview(target: string, more: Partial<Overview> = {}): Overview {
  return {
    target,
    instance: "my-sqlserver",
    status: "available",
    engine: "sqlserver-se",
    engineVersion: "14.00.3465.1.v1",
    class: "db.m6i.large",
    allocatedGiB: 200,
    maxAllocatedGiB: 500,
    freeBytes: 97.3 * 1024 ** 3,
    sql: "skipped",
    sqlDetail: "SQL password: sql.password is prompt: set DIALGA_SQL_PASSWORD",
    ...more,
  };
}

/** A user database as GET /databases lists it (sizes in MiB). */
export function database(name: string, more: Partial<Database> = {}): Database {
  return {
    name,
    state: "ONLINE",
    recovery: "FULL",
    compatibility: 140,
    owner: "admin",
    userAccess: "MULTI_USER",
    readOnly: false,
    createdAt: "2026-10-15T13:05:40",
    dataMB: 16,
    logMB: 8,
    ...more,
  };
}

/** GET /prerequisites: the plugin found, no container runtime. */
export const PLUGIN_FOUND: Prerequisite = {
  id: "session-manager-plugin",
  program: "session-manager-plugin",
  found: true,
  path: "/usr/local/bin/session-manager-plugin",
  version: "1.2.707.0",
  purpose: "SSM tunnels",
  install: [],
};

export const RUNTIME_MISSING: Prerequisite = {
  id: "container-runtime",
  program: "podman",
  found: false,
  purpose: "inspect .bak files",
  install: [
    { distro: "fedora", command: "sudo dnf install podman" },
    { distro: "debian", command: "sudo apt install podman" },
  ],
};

/** An instance as the discovery lists it (placeholder endpoint). */
export function discovered(
  identifier: string,
  more: Partial<DiscoveredInstance> = {},
): DiscoveredInstance {
  return {
    identifier,
    endpoint: `${identifier}.abc123.eu-west-1.rds.amazonaws.com`,
    port: 1433,
    engine: "sqlserver-se",
    engineVersion: "14.00.3465.1.v1",
    class: "db.m6i.large",
    status: "available",
    masterUser: "admin",
    optionGroup: "sqlserver-backup",
    backupRestore: true,
    ...more,
  };
}

/** Renders inside the I18nProvider, with lang saved as the user's choice. */
export function renderI18n(ui: ReactElement, lang: Lang = "en", strict = false) {
  localStorage.setItem("dialga.lang", lang);
  return render(ui, { wrapper: I18nProvider, reactStrictMode: strict });
}

import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import type { TargetSummary, Task } from "../api";
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
  importPrefix: "import",
  exportPrefix: "export",
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

/** Renders inside the I18nProvider, with lang saved as the user's choice. */
export function renderI18n(ui: ReactElement, lang: Lang = "en", strict = false) {
  localStorage.setItem("dialga.lang", lang);
  return render(ui, { wrapper: I18nProvider, reactStrictMode: strict });
}

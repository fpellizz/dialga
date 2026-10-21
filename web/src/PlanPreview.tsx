import { ShieldAlert } from "lucide-react";
import { useId, useLayoutEffect, useRef } from "react";
import type { StepEvent } from "./api";
import { type MessageKey, messages, type Translate, useI18n } from "./i18n";
import {
  isAccessRequest,
  isDestructive,
  type PlanPreview,
  type PlanRequest,
  planStepKey,
  stepKeys,
} from "./plans";
import { StepList } from "./StepList";

/** Failed checks the caller expects (the backup is uploaded later): step id → why, translated. */
export type PendingChecks = Partial<Record<string, string>>;

const REPLACE_ACTION = {
  "single-user": "planaction.singleUser",
  "rename-old": "planaction.renameOld",
  "rename-new": "planaction.renameNew",
  "multi-user": "planaction.multiUser",
  "multi-user-old": "planaction.multiUserOld",
  post: "planaction.post",
} as const;

const ACCESS_CHECK: Record<string, MessageKey> = {
  login: "plancheck.login",
  user: "plancheck.user",
  schemas: "plancheck.schemas",
  access: "plancheck.access",
  backup: "plancheck.backup",
  msdb: "plancheck.msdb",
  "msdb-user": "plancheck.msdbUser",
};

/** The label of a check, an action or the wait, in the user's language. */
export function stepLabel(
  t: Translate,
  s: StepEvent,
  request: PlanRequest,
  strategy?: string,
): string {
  const vars = {
    database: request.database ?? "",
    key: request.key ?? "",
    id: request.taskId ?? "",
  };
  // The steps of a job are prefixed with the database: <db>/restore.
  const id = s.step.slice(s.step.lastIndexOf("/") + 1);
  if (isAccessRequest(request)) {
    // "backup" is also the safety backup of a drop: not here. The actions
    // (add-role, create-user...) keep the engine's title, which names the role and the user.
    const key = ACCESS_CHECK[id];
    return key ? t(key) : s.label;
  }
  switch (id) {
    case "restore":
      // A swap restores beside the current database first, under a temporary name.
      return strategy === "swap"
        ? t("planaction.restoreTemp", vars)
        : t("planaction.restore", vars);
    case "backup":
      return t("planaction.backupFirst", vars);
    case "single-user":
    case "rename-old":
    case "rename-new":
    case "multi-user":
    case "multi-user-old":
    case "post":
      return t(REPLACE_ACTION[id], vars);
    case "export":
    case "finish":
    case "cancel":
    case "drop":
      return t(`planaction.${id}`, vars);
  }
  const key = planStepKey(id);
  return key ? t(key) : s.label;
}

/**
 * "swap" when the steps include the rename of the restored database, "drop"
 * for a replace that drops first, "" otherwise: a job does not say it.
 */
export function replaceStrategy(ids: readonly string[]): string {
  const plain = ids.map((id) => id.slice(id.lastIndexOf("/") + 1).replace(/#[0-9]+$/, ""));
  if (plain.includes("rename-new")) {
    return "swap";
  }
  return plain.includes("drop") && plain.includes("restore") ? "drop" : "";
}

/** Every failed check is one the caller expects: the plan is ready for it. */
export function readyWith(preview: PlanPreview, pending: PendingChecks = {}): boolean {
  const failed = preview.checks.filter((c) => c.status === "failed");
  return preview.ready || (failed.length > 0 && failed.every((c) => pending[c.step] !== undefined));
}

/**
 * A plan as the engine previewed it, inline: the pre-flight checks, the
 * actions with their exact T-SQL behind Details, the protected-target and
 * drop warnings, and the confirmation field when the plan asks for one.
 * Running it is the caller's (PlanDialog, the import wizard).
 */
export function PlanPreviewView({
  preview,
  title,
  confirm = "",
  onConfirmChange,
  onSubmit,
  showConfirm = true,
  pending = {},
  autoFocus = true,
}: {
  preview: PlanPreview;
  /** A heading above the plan (the wizard shows several); the sections go one level down. */
  title?: string;
  /** The text typed in the confirmation field. */
  confirm?: string;
  onConfirmChange?: (text: string) => void;
  /** Enter in the confirmation field. */
  onSubmit?: () => void;
  /** false: the caller asks for the confirmation itself (once for several plans). */
  showConfirm?: boolean;
  /** Failed checks shown as expected (skipped, with this text) instead of failed. */
  pending?: PendingChecks;
  /** Focus the confirmation field when the preview arrives (default true). */
  autoFocus?: boolean;
}) {
  const { t, lang } = useI18n();
  const ids = useId();
  const input = useRef<HTMLInputElement>(null);
  const ready = readyWith(preview, pending);
  const destructive = isDestructive(preview.plan);
  const database = preview.request.database ?? "";
  const checks = preview.checks.map((c) => {
    const why = pending[c.step];
    return c.status === "failed" && why !== undefined
      ? { ...c, status: "skipped" as const, detail: why }
      : c;
  });
  // An id can repeat (two add-role): the key is the id and its occurrence.
  const actionKeys = stepKeys(preview.plan.actions.map((a) => ({ step: a.id })));
  const asking = showConfirm && !!onConfirmChange && !!preview.confirmation;
  const Heading = title ? "h4" : "h3";
  const label = (s: StepEvent) => stepLabel(t, s, preview.request, preview.replace?.strategy);
  const [confirmBefore, confirmAfter] = messages[lang]["plan.confirm.label"].split("{text}");

  // The plan asks to retype something: that is what the user does next
  // (in the commit itself, so the focus never lands elsewhere first).
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new preview (after a 412) asks again.
  useLayoutEffect(() => {
    if (autoFocus && asking) {
      input.current?.focus();
    }
  }, [preview, autoFocus, asking]);

  return (
    <>
      {title && <h3 className="plan-title">{title}</h3>}
      {preview.plan.protected && (
        <div className="banner banner-warn">
          <ShieldAlert size={16} aria-hidden="true" />
          <span className="banner-text">
            <strong>{t("plan.protected")}</strong> ·{" "}
            {t("plan.confirm.protected", { target: preview.plan.target })}
          </span>
        </div>
      )}
      <section aria-labelledby={`${ids}-checks`}>
        <Heading id={`${ids}-checks`} className="plan-heading">
          {t("plan.checks")}
        </Heading>
        <StepList steps={checks} label={label} />
      </section>
      {!ready && (
        <div className="banner banner-fail">
          <span className="banner-text">{t("plan.notReady")}</span>
        </div>
      )}
      <section aria-labelledby={`${ids}-actions`}>
        <Heading id={`${ids}-actions`} className="plan-heading">
          {t("plan.actions")}
        </Heading>
        <ol className="plan-actions">
          {preview.plan.actions.map((a, i) => (
            <li key={actionKeys[i]} className="plan-action">
              <div className="plan-action-head">
                <span className="step-label">
                  {label({ step: a.id, label: a.title, status: "ok" })}
                </span>
                {a.risk === "destructive" && (
                  <span className="badge badge-failed">{t("plan.destructive")}</span>
                )}
              </div>
              <details className="task-details">
                <summary>{t("plan.details")}</summary>
                <pre className="statement">{a.statement}</pre>
              </details>
            </li>
          ))}
        </ol>
      </section>
      {asking && ready && (
        <div className="plan-confirm">
          {destructive && !preview.plan.protected && (
            <p id={`${ids}-warning`} className="plan-warning">
              {preview.plan.operation === "revoke"
                ? t("plan.confirm.dropUser", { database, target: preview.plan.target })
                : t("plan.confirm.drop", { database, target: preview.plan.target })}
            </p>
          )}
          <label className="field-inline">
            <span>
              {confirmBefore}
              <code className="confirm-text">{preview.confirmation}</code>
              {confirmAfter}
            </span>
            <input
              ref={input}
              className="input mono"
              name="confirm"
              aria-describedby={
                destructive && !preview.plan.protected ? `${ids}-warning` : undefined
              }
              value={confirm}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => onConfirmChange?.(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && onSubmit) {
                  e.preventDefault();
                  onSubmit();
                }
              }}
            />
          </label>
        </div>
      )}
    </>
  );
}

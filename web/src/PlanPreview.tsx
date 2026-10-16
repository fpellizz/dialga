import { ShieldAlert } from "lucide-react";
import { useId, useLayoutEffect, useRef } from "react";
import { errorText, type StepEvent } from "./api";
import { type MessageKey, messages, type Translate, useI18n } from "./i18n";
import { errorHint, isDestructive, type PlanPreview, type PlanRequest, planStepKey } from "./plans";
import { StepList } from "./StepList";

/** Failed checks the caller expects (the backup is uploaded later): step id → message. */
export type PendingChecks = Partial<Record<string, MessageKey>>;

/** The label of a check, an action or the wait, in the user's language. */
export function stepLabel(t: Translate, s: StepEvent, request: PlanRequest): string {
  const vars = {
    database: request.database ?? "",
    key: request.key ?? "",
    id: request.taskId ?? "",
  };
  switch (s.step) {
    case "restore":
    case "export":
    case "finish":
    case "cancel":
    case "drop":
      return t(`planaction.${s.step}`, vars);
  }
  const key = planStepKey(s.step);
  return key ? t(key) : s.label;
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
  confirm,
  onConfirmChange,
  onSubmit,
  pending = {},
  autoFocus = true,
}: {
  preview: PlanPreview;
  /** The text typed in the confirmation field. */
  confirm: string;
  onConfirmChange: (text: string) => void;
  /** Enter in the confirmation field. */
  onSubmit?: () => void;
  /** Failed checks shown as expected (skipped, with this message) instead of failed. */
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
    const key = pending[c.step];
    return c.status === "failed" && key ? { ...c, status: "skipped" as const, detail: t(key) } : c;
  });
  const label = (s: StepEvent) => stepLabel(t, s, preview.request);
  const [confirmBefore, confirmAfter] = messages[lang]["plan.confirm.label"].split("{text}");

  // The plan asks to retype something: that is what the user does next
  // (in the commit itself, so the focus never lands elsewhere first).
  useLayoutEffect(() => {
    if (autoFocus && preview.confirmation) {
      input.current?.focus();
    }
  }, [preview, autoFocus]);

  return (
    <>
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
        <h3 id={`${ids}-checks`} className="plan-heading">
          {t("plan.checks")}
        </h3>
        <StepList steps={checks} label={label} />
      </section>
      {!ready && (
        <div className="banner banner-fail">
          <span className="banner-text">{t("plan.notReady")}</span>
        </div>
      )}
      <section aria-labelledby={`${ids}-actions`}>
        <h3 id={`${ids}-actions`} className="plan-heading">
          {t("plan.actions")}
        </h3>
        <ol className="plan-actions">
          {preview.plan.actions.map((a) => (
            <li key={a.id} className="plan-action">
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
      {preview.confirmation && ready && (
        <div className="plan-confirm">
          {destructive && !preview.plan.protected && (
            <p id={`${ids}-warning`} className="plan-warning">
              {t("plan.confirm.drop", { database, target: preview.plan.target })}
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
              onChange={(e) => onConfirmChange(e.currentTarget.value)}
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

/** An engine error with what to do about it, by HTTP status. */
export function ErrorBanner({ error }: { error: unknown }) {
  const { t } = useI18n();
  const hint = errorHint(error);
  return (
    <div className="banner banner-fail" role="alert">
      <span className="banner-text">{errorText(error)}</span>
      {hint && <span className="banner-hint">{t(hint)}</span>}
    </div>
  );
}

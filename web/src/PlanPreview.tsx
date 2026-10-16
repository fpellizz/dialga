import type { StepEvent } from "./api";
import { useI18n } from "./i18n";
import type { PlanPreview as Preview } from "./plans";
import { StepList } from "./StepList";

/**
 * A plan before it runs, inline: the actions with their risk, the pre-flight
 * checks and the dry-run text. pending turns failed checks into "skipped"
 * with an explanation (a restore's source check before the upload).
 */
export function PlanPreview({
  preview,
  title,
  pending = {},
}: {
  preview: Preview;
  title: string;
  /** Step id → why its failure is expected now. */
  pending?: Record<string, string>;
}) {
  const { t } = useI18n();
  const checks: StepEvent[] = preview.checks.map((c) =>
    c.status === "failed" && pending[c.step] !== undefined
      ? { ...c, status: "skipped", detail: pending[c.step] }
      : c,
  );
  return (
    <div className="plan">
      <h3>{title}</h3>
      <ul className="plan-actions">
        {preview.plan.actions.map((action) => (
          <li key={action.id} className="plan-action">
            <span
              className={`badge ${action.risk === "destructive" ? "badge-failed" : "badge-running"}`}
            >
              {t(action.risk === "destructive" ? "plan.risk.destructive" : "plan.risk.change")}
            </span>
            <span className="plan-action-title">{action.title}</span>
          </li>
        ))}
      </ul>
      {checks.length > 0 && <StepList steps={checks} />}
      <details className="task-details">
        <summary>{t("plan.dryRun")}</summary>
        <pre className="task-info">{preview.preview.trim()}</pre>
      </details>
    </div>
  );
}

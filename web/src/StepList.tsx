import { CircleCheck, CircleMinus, CircleX, LoaderCircle, TriangleAlert } from "lucide-react";
import type { StepEvent, StepStatus } from "./api";
import { type MessageKey, stepKey, useI18n } from "./i18n";

const STATUS_KEY: Record<StepStatus, MessageKey> = {
  running: "status.running",
  ok: "status.ok",
  warning: "status.warning",
  failed: "status.failed",
  skipped: "status.skipped",
};

function StepIcon({ status }: { status: StepStatus }) {
  switch (status) {
    case "running":
      return <LoaderCircle size={18} className="spin tone-info" aria-hidden="true" />;
    case "ok":
      return <CircleCheck size={18} className="tone-success" aria-hidden="true" />;
    case "warning":
      return <TriangleAlert size={18} className="tone-warning" aria-hidden="true" />;
    case "failed":
      return <CircleX size={18} className="tone-destructive" aria-hidden="true" />;
    default:
      return <CircleMinus size={18} className="tone-muted" aria-hidden="true" />;
  }
}

/**
 * The steps of a check, in the order the engine reported them. label names
 * a step (default: the check step ids, then the engine label).
 */
export function StepList({
  steps,
  label,
}: {
  steps: StepEvent[];
  label?: (step: StepEvent) => string;
}) {
  const { t } = useI18n();
  return (
    <ul className="steps">
      {steps.map((s) => {
        const key = stepKey(s.step);
        return (
          <li key={s.step} className="step">
            <StepIcon status={s.status} />
            <div className="step-body">
              <div className="step-label">{label ? label(s) : key ? t(key) : s.label}</div>
              {s.detail && <div className="step-detail">{s.detail}</div>}
            </div>
            <span className={`badge badge-${s.status}`}>{t(STATUS_KEY[s.status])}</span>
          </li>
        );
      })}
    </ul>
  );
}

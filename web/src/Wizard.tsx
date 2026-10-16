import { Check } from "lucide-react";
import { type ReactNode, type RefObject, useEffect, useId, useRef } from "react";
import { useI18n } from "./i18n";

export interface WizardStep {
  id: string;
  label: string;
}

/**
 * The shell of a guided procedure (Export, Import): the page title, a
 * stepper, the panel of the current step and its footer. The page owns the
 * state and decides when a step is valid; the shell gives the structure and
 * the keyboard and screen reader behaviour:
 *   - the stepper is an ordered list, the current step aria-current="step";
 *     the steps before it are buttons to go back to, unless locked;
 *   - the panel is a region named by its heading ("Step 2 of 6: Options"),
 *     which takes the focus when the step changes (not on the first render);
 *     titleRef lets the page focus it too, when a step changes in place;
 *   - busy marks the panel while it loads (aria-busy);
 *   - footer holds the actions of the step (Back, Next, extra ones): a Next
 *     of type="submit" with form= can submit a <form> in the panel.
 */
export function Wizard({
  title,
  lead,
  steps,
  current,
  onStep,
  locked = false,
  busy = false,
  titleRef,
  footer,
  children,
}: {
  title: string;
  lead?: string;
  steps: WizardStep[];
  current: number;
  /** Go back to an earlier step from the stepper. */
  onStep?: (index: number) => void;
  /** The earlier steps cannot be reopened (while something runs). */
  locked?: boolean;
  busy?: boolean;
  titleRef?: RefObject<HTMLHeadingElement | null>;
  footer?: ReactNode;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const ids = useId();
  const own = useRef<HTMLHeadingElement>(null);
  const heading = titleRef ?? own;
  const shown = useRef(current);
  const step = steps[current];
  const count = t("wizard.count", { n: current + 1, total: steps.length });

  // A new step moves the focus to its heading, as a new page would.
  useEffect(() => {
    if (shown.current === current) {
      return;
    }
    shown.current = current;
    heading.current?.focus();
  }, [current, heading]);

  return (
    <div className="page wizard">
      <header className="page-head">
        <div>
          <h1>{title}</h1>
          {lead && <p className="lead">{lead}</p>}
        </div>
      </header>

      <nav aria-label={t("wizard.steps")}>
        <ol className="wizard-steps">
          {steps.map((s, i) => {
            const done = i < current;
            const dot = (
              <span className="wizard-dot" aria-hidden="true">
                {done ? <Check size={12} /> : i + 1}
              </span>
            );
            let state: string | undefined;
            if (i === current) {
              state = "current";
            } else if (done) {
              state = "done";
            }
            return (
              <li key={s.id} className={state}>
                {done && onStep && !locked ? (
                  <button
                    type="button"
                    className="wizard-jump"
                    onClick={() => onStep(i)}
                    aria-label={t("wizard.goTo", { step: s.label })}
                  >
                    {dot}
                    {s.label}
                  </button>
                ) : (
                  <span aria-current={i === current ? "step" : undefined}>
                    {dot}
                    {s.label}
                    {done && <span className="sr-only"> {t("wizard.done")}</span>}
                  </span>
                )}
              </li>
            );
          })}
        </ol>
      </nav>

      <section className="card wizard-panel" aria-labelledby={`${ids}-title`} aria-busy={busy}>
        {/* One name for heading and region: "Step 2 of 6: Options". */}
        <h2
          id={`${ids}-title`}
          ref={heading}
          tabIndex={-1}
          className="wizard-title"
          aria-label={`${count}: ${step?.label ?? ""}`}
        >
          <span className="wizard-count">{count}</span>
          <span>{step?.label}</span>
        </h2>
        <div className="wizard-body">{children}</div>
        {footer && <div className="wizard-nav">{footer}</div>}
      </section>
    </div>
  );
}

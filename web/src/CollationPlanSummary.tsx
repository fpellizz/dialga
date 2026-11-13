import type { ElementType } from "react";
import { useId } from "react";
import { type CollationPlan, plainTableRef } from "./collation";
import { useI18n } from "./i18n";

/**
 * What a collation plan changes, above its actions: the target, the counts
 * of a convert-columns (units, tables, columns, indexes, foreign keys, data
 * size) and the tables it leaves as they are, each with its reason.
 */
export function CollationPlanSummary({
  plan,
  Heading = "h3",
}: {
  plan: CollationPlan;
  Heading?: ElementType;
}) {
  const { t, lang } = useI18n();
  const id = useId();
  const refused = plan.refused ?? [];
  const convert = plan.units > 0 || plan.tables > 0;
  const size = new Intl.NumberFormat(lang, { maximumFractionDigits: 1 }).format(plan.dataMB);
  return (
    <section aria-labelledby={`${id}-title`} className="collation-plan">
      <Heading id={`${id}-title`} className="plan-heading">
        {t("collation.plan.title")}
      </Heading>
      <p className="collation-plan-line">
        {plan.from
          ? t("collation.plan.change", { from: plan.from, to: plan.to })
          : t("collation.plan.to", { to: plan.to })}
      </p>
      {convert && (
        <p className="collation-plan-line">
          {t("collation.plan.summary", {
            units: plan.units,
            tables: plan.tables,
            columns: plan.columns,
            indexes: plan.indexes,
            foreignKeys: plan.foreignKeys,
            size,
          })}
        </p>
      )}
      {refused.length > 0 && (
        <>
          <p className="collation-plan-line">
            {t("collation.plan.refused", { count: refused.length })}
          </p>
          <ul className="collation-refused">
            {refused.map((r) => (
              <li key={`${r.schema}.${r.name}`}>
                <span className="mono">{plainTableRef(r)}</span>
                <span className="muted">
                  {" · "}
                  {t("collation.plan.refusedReason", { reason: r.reason })}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

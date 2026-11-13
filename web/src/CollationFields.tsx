import { TriangleAlert } from "lucide-react";
import { useEffect, useId, useMemo, useState } from "react";
import { CollationPicker, type Suggestion } from "./CollationPicker";
import {
  type DatabaseCollationView,
  getDatabaseCollation,
  MAX_TABLES,
  plainTableRef,
  type TableRef,
  tablesOf,
  validCollationName,
} from "./collation";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { usePassword } from "./PasswordProvider";
import { type PlanRequest, planHint } from "./plans";

/** Whether the fields hold enough for a preview (the picker also refuses a name it knows is not there). */
export function collationComplete(
  operation: "set-collation" | "convert-columns",
  p: PlanRequest,
  known: boolean | undefined,
): boolean {
  const collation = p.collation?.trim() ?? "";
  if (operation === "set-collation") {
    return validCollationName(collation) && known !== false;
  }
  // "" = the default of the database.
  const collationOk = collation === "" || (validCollationName(collation) && known !== false);
  // tables: [] = "some tables" with none ticked yet.
  const tablesOk = p.tables === undefined || (p.tables.length > 0 && p.tables.length <= MAX_TABLES);
  return collationOk && tablesOk;
}

function sameTable(a: TableRef, b: TableRef): boolean {
  return a.schema === b.schema && a.name === b.name;
}

/**
 * The inputs of a collation plan. set-collation: the new default collation
 * (with the explanation of what it changes and what it does not).
 * convert-columns: the target (the database default or another collation),
 * the tables (all, or ticked from the columns that differ) and whether to
 * skip the data checks.
 */
export function CollationFields({
  engine,
  target,
  database,
  operation,
  params,
  onChange,
  disabled,
}: {
  engine: EngineInfo;
  target: string;
  database: string;
  operation: "set-collation" | "convert-columns";
  params: PlanRequest;
  onChange: (patch: Partial<PlanRequest>) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const ids = useId();
  const [view, setView] = useState<DatabaseCollationView | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [tableFilter, setTableFilter] = useState("");
  const [customTarget, setCustomTarget] = useState(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: epoch is the retry trigger (the SQL password was saved).
  useEffect(() => {
    const controller = new AbortController();
    setLoadError(null);
    getDatabaseCollation(engine, target, database, controller.signal).then(
      (v) => !controller.signal.aborted && setView(v),
      (e: unknown) => !controller.signal.aborted && setLoadError(e),
    );
    return () => controller.abort();
  }, [engine, target, database, epoch]);

  const suggestions = useMemo<Suggestion[]>(() => {
    const list: Suggestion[] = [];
    if (view?.server) {
      list.push({ name: view.server, kind: "server" });
    }
    if (view?.collation && view.collation !== view.server && operation === "convert-columns") {
      list.push({ name: view.collation, kind: "database" });
    }
    return list;
  }, [view, operation]);

  if (operation === "set-collation") {
    return (
      <div className="collation-fields">
        {loadError !== null && <ErrorBanner error={loadError} fallback={planHint(loadError)} />}
        <CollationPicker
          id={`${ids}-collation`}
          engine={engine}
          target={target}
          value={params.collation ?? ""}
          onChange={(collation) => onChange({ collation })}
          label={t("collation.fields.newDefault")}
          hint={t("collation.fields.newDefaultHint")}
          suggestions={suggestions}
          disabled={disabled}
        />
        <p className="hint collation-keeps">
          {t("collation.fields.keepsColumns", { collation: view?.collation ?? "" })}
        </p>
      </div>
    );
  }

  const custom = customTarget || (params.collation ?? "") !== "";
  const tables = tablesOf(view?.columns.filter((c) => !c.unsupported) ?? []);
  const needle = tableFilter.trim().toLowerCase();
  const shown = tables.filter((r) => !needle || plainTableRef(r).toLowerCase().includes(needle));
  const picked = params.tables ?? [];
  // [] is "some tables, none ticked yet" (the plan waits); undefined is every table.
  const scoped = params.tables !== undefined;

  return (
    <div className="collation-fields">
      {loadError !== null && <ErrorBanner error={loadError} fallback={planHint(loadError)} />}
      <fieldset className="form-section collation-fieldset">
        <legend>{t("collation.fields.target")}</legend>
        <div className="check">
          <input
            id={`${ids}-default`}
            type="radio"
            name={`${ids}-target`}
            checked={!custom}
            disabled={disabled}
            onChange={() => {
              setCustomTarget(false);
              onChange({ collation: "" });
            }}
          />
          <label htmlFor={`${ids}-default`}>
            {t("collation.fields.targetDefault", { collation: view?.collation ?? "…" })}
          </label>
        </div>
        <div className="check">
          <input
            id={`${ids}-other`}
            type="radio"
            name={`${ids}-target`}
            checked={custom}
            disabled={disabled}
            onChange={() => setCustomTarget(true)}
          />
          <label htmlFor={`${ids}-other`}>{t("collation.fields.targetOther")}</label>
        </div>
        {custom && (
          <CollationPicker
            id={`${ids}-collation`}
            engine={engine}
            target={target}
            value={params.collation ?? ""}
            onChange={(collation) => onChange({ collation })}
            label={t("collation.picker.label")}
            hint={t("collation.fields.targetOtherHint")}
            suggestions={suggestions}
            disabled={disabled}
          />
        )}
      </fieldset>

      <fieldset className="form-section collation-fieldset">
        <legend>{t("collation.fields.tables")}</legend>
        <div className="check">
          <input
            id={`${ids}-all`}
            type="radio"
            name={`${ids}-scope`}
            checked={!scoped}
            disabled={disabled}
            onChange={() => onChange({ tables: undefined })}
          />
          <label htmlFor={`${ids}-all`}>{t("collation.fields.allTables")}</label>
        </div>
        <div className="check">
          <input
            id={`${ids}-some`}
            type="radio"
            name={`${ids}-scope`}
            checked={scoped}
            disabled={disabled || tables.length === 0}
            onChange={() => onChange({ tables: [] })}
          />
          <label htmlFor={`${ids}-some`}>{t("collation.fields.someTables")}</label>
        </div>
        {tables.length === 0 && view && <p className="hint">{t("collation.fields.noTables")}</p>}
        {scoped && (
          <>
            <label className="field-inline">
              <span>{t("collation.columns.filter")}</span>
              <input
                type="search"
                name="tableFilter"
                className="input"
                value={tableFilter}
                disabled={disabled}
                onChange={(e) => setTableFilter(e.currentTarget.value)}
              />
            </label>
            <fieldset className="collation-tables">
              <legend className="sr-only">{t("collation.fields.tablesList")}</legend>
              {shown.map((r) => {
                const on = picked.some((p) => sameTable(p, r));
                return (
                  <label key={plainTableRef(r)} className="check">
                    <input
                      type="checkbox"
                      name="table"
                      checked={on}
                      disabled={disabled}
                      onChange={(e) => {
                        const checked = e.currentTarget.checked;
                        onChange({
                          tables: checked ? [...picked, r] : picked.filter((p) => !sameTable(p, r)),
                        });
                      }}
                    />
                    <span className="mono">{plainTableRef(r)}</span>
                  </label>
                );
              })}
            </fieldset>
            <p className="hint" role="status">
              {t("collation.fields.picked", { count: picked.length })}
            </p>
            {picked.length > MAX_TABLES && (
              <p className="field-error">{t("collation.fields.tooMany", { max: MAX_TABLES })}</p>
            )}
            <p className="hint">{t("collation.fields.closureHint")}</p>
          </>
        )}
      </fieldset>

      <div className="check">
        <input
          id={`${ids}-skip`}
          type="checkbox"
          name="skipDataChecks"
          checked={!!params.skipDataChecks}
          disabled={disabled}
          aria-describedby={`${ids}-skip-hint`}
          onChange={(e) => onChange({ skipDataChecks: e.currentTarget.checked })}
        />
        <div>
          <label htmlFor={`${ids}-skip`}>{t("collation.fields.skipDataChecks")}</label>
          <p id={`${ids}-skip-hint`} className="hint">
            {t("collation.fields.skipDataChecksHint")}
          </p>
          {params.skipDataChecks && (
            <p className="collation-warning" role="status">
              <TriangleAlert size={14} aria-hidden="true" />
              {t("collation.fields.skipDataChecksWarn")}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}

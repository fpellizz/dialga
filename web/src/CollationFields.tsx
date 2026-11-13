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
  /** "Another collation" is selected: an empty picker is not "the database default". */
  customTarget = false,
): boolean {
  const collation = p.collation?.trim() ?? "";
  if (operation === "set-collation") {
    return validCollationName(collation) && known !== false;
  }
  // "" = the default of the database, unless "Another collation" is selected.
  const collationOk =
    collation === "" ? !customTarget : validCollationName(collation) && known !== false;
  // tables: [] = "some tables" with none ticked yet.
  const tablesOk = p.tables === undefined || (p.tables.length > 0 && p.tables.length <= MAX_TABLES);
  return collationOk && tablesOk;
}

function sameTable(a: TableRef, b: TableRef): boolean {
  return a.schema === b.schema && a.name === b.name;
}

/** A key for a table: names may hold dots, so they are not joined with one. */
function tableKey(r: TableRef): string {
  return `${r.schema}\u0000${r.name}`;
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
  customTarget: customProp,
  onCustomTarget,
  disabled,
}: {
  engine: EngineInfo;
  target: string;
  database: string;
  operation: "set-collation" | "convert-columns";
  params: PlanRequest;
  onChange: (patch: Partial<PlanRequest>) => void;
  /** "Another collation" is selected; the dialog keeps it to know the form is incomplete. */
  customTarget?: boolean;
  onCustomTarget?: (custom: boolean) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const ids = useId();
  const [view, setView] = useState<DatabaseCollationView | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [tableFilter, setTableFilter] = useState("");
  const [customLocal, setCustomLocal] = useState(false);
  const customTarget = customProp ?? customLocal;
  const setCustomTarget = (custom: boolean) => {
    setCustomLocal(custom);
    onCustomTarget?.(custom);
  };
  // Another collation than the default of the database: every table is converted.
  const collationNow = (params.collation ?? "").trim();
  const customDiffers =
    operation === "convert-columns" &&
    (customTarget || collationNow !== "") &&
    !(view && collationNow !== "" && collationNow.toLowerCase() === view.collation.toLowerCase());
  const scopedNow = params.tables !== undefined;

  // The checklist lists only the columns that differ from the default, so it cannot
  // scope a conversion to another collation: back to every table.
  // biome-ignore lint/correctness/useExhaustiveDependencies: onChange is a setter of the parent.
  useEffect(() => {
    if (customDiffers && scopedNow) {
      onChange({ tables: undefined });
    }
  }, [customDiffers, scopedNow]);

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
          {view
            ? t("collation.fields.keepsColumns", { collation: view.collation })
            : t("collation.fields.keepsColumnsUnknown")}
        </p>
      </div>
    );
  }

  const custom = customTarget || (params.collation ?? "") !== "";
  const tables = tablesOf(view?.columns ?? []);
  // A table with a column the engine never converts is refused as a whole: it cannot be ticked.
  const blocked = new Map<string, string>();
  for (const c of view?.columns ?? []) {
    if (c.unsupported && !blocked.has(tableKey({ schema: c.schema, name: c.table }))) {
      blocked.set(tableKey({ schema: c.schema, name: c.table }), c.unsupported);
    }
  }
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
        <legend>
          {customDiffers ? t("collation.fields.tables") : t("collation.fields.tablesOutside")}
        </legend>
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
            disabled={disabled || tables.length === 0 || customDiffers}
            onChange={() => onChange({ tables: [] })}
          />
          <label htmlFor={`${ids}-some`}>{t("collation.fields.someTables")}</label>
        </div>
        {customDiffers && <p className="hint">{t("collation.fields.customNote")}</p>}
        {!customDiffers && tables.length === 0 && view && (
          <p className="hint">{t("collation.fields.noTables")}</p>
        )}
        {!customDiffers && view?.truncated && (
          <p className="hint" role="status">
            {t("collation.fields.truncatedTables")}
          </p>
        )}
        {scoped && !customDiffers && (
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
                const key = tableKey(r);
                const dom = `${ids}-${tables.indexOf(r)}`;
                const on = picked.some((p) => sameTable(p, r));
                const why = blocked.get(key);
                return (
                  <div key={key} className="check">
                    <input
                      id={`${dom}-t`}
                      type="checkbox"
                      name="table"
                      checked={on}
                      disabled={disabled || why !== undefined}
                      aria-describedby={why !== undefined ? `${dom}-w` : undefined}
                      onChange={(e) => {
                        const checked = e.currentTarget.checked;
                        onChange({
                          tables: checked ? [...picked, r] : picked.filter((p) => !sameTable(p, r)),
                        });
                      }}
                    />
                    <label htmlFor={`${dom}-t`} className="mono">
                      {plainTableRef(r)}
                    </label>
                    {why !== undefined && (
                      <span
                        id={`${dom}-w`}
                        className="badge badge-warning"
                        title={`${t("collation.fields.unsupportedTable")} ${why}`}
                      >
                        {t("collation.unsupported")}
                        <span className="sr-only">
                          : {t("collation.fields.unsupportedTable")} {why}
                        </span>
                      </span>
                    )}
                  </div>
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

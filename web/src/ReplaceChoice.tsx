import { TriangleAlert } from "lucide-react";
import { useId } from "react";
import { type MessageKey, useI18n } from "./i18n";
import type { ReplaceMode } from "./plans";

const OPTIONS: { mode: ReplaceMode; label: MessageKey; hint: MessageKey }[] = [
  { mode: "", label: "replace.stop", hint: "replace.stopHint" },
  { mode: "auto", label: "replace.auto", hint: "replace.autoHint" },
  { mode: "swap", label: "replace.swap", hint: "replace.swapHint" },
  { mode: "drop", label: "replace.drop", hint: "replace.dropHint" },
];

/**
 * What to do when the destination database already exists: stop (the
 * default), replace it letting the engine choose, keep the old one (swap) or
 * drop it first (optionally backing it up to S3). The caller shows it only
 * for a name that exists; the engine checks it again in the preview.
 */
export function ReplaceChoice({
  database,
  mode,
  backupFirst,
  onChange,
  disabled,
}: {
  database: string;
  mode: ReplaceMode;
  backupFirst: boolean;
  onChange: (change: { replace: ReplaceMode; backupFirst: boolean }) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const ids = useId();
  return (
    <div className="form-section replace-choice">
      <p id={`${ids}-legend`} className="replace-legend">
        <strong>{t("replace.legend", { db: database })}</strong>
      </p>
      <div role="radiogroup" aria-labelledby={`${ids}-legend`}>
        {OPTIONS.map((o) => (
          <div key={o.mode || "stop"} className="check">
            <input
              id={`${ids}-${o.mode || "stop"}`}
              type="radio"
              name={`${ids}-replace`}
              checked={mode === o.mode}
              disabled={disabled}
              aria-describedby={`${ids}-${o.mode || "stop"}-hint`}
              onChange={() => onChange({ replace: o.mode, backupFirst })}
            />
            <div>
              <label htmlFor={`${ids}-${o.mode || "stop"}`}>{t(o.label)}</label>
              <p id={`${ids}-${o.mode || "stop"}-hint`} className="hint">
                {t(o.hint, { db: database })}
              </p>
            </div>
          </div>
        ))}
      </div>
      {mode === "drop" && (
        <div className="indent">
          <label className="check">
            <input
              type="checkbox"
              name="backupFirst"
              checked={backupFirst}
              disabled={disabled}
              onChange={(e) => onChange({ replace: mode, backupFirst: e.currentTarget.checked })}
            />
            <span>{t("replace.backupFirst")}</span>
          </label>
          {!backupFirst && (
            <div className="banner banner-warn" role="alert">
              <TriangleAlert size={16} aria-hidden="true" />
              <span className="banner-text">{t("replace.noBackupWarning", { db: database })}</span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

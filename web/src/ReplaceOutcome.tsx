import { Trash2 } from "lucide-react";
import { useI18n } from "./i18n";
import { type ReplaceMode, replaceOutcome } from "./plans";

/**
 * What a replace left behind: the old database a swap kept, with the action
 * to drop it, and the S3 URI of the safety backup a drop took. texts are what
 * the run reported (the names are read from them, see replaceOutcome).
 */
export function ReplaceOutcome({
  database,
  mode,
  texts,
  bucket,
  onDropOld,
}: {
  database: string;
  mode: ReplaceMode;
  texts: readonly (string | undefined)[];
  bucket?: string;
  /** Drop the kept database: opens its drop plan. */
  onDropOld?: (database: string) => void;
}) {
  const { t } = useI18n();
  const { kept, backup } = replaceOutcome(database, texts, bucket);
  if (!kept && !backup && mode !== "swap") {
    return null;
  }
  return (
    <section className="replace-outcome" aria-label={t("replace.outcome")}>
      {kept ? (
        <div className="banner banner-ok">
          <span className="banner-text">{t("replace.keptOld", { old: kept })}</span>
          {onDropOld && (
            <button type="button" className="btn btn-outline" onClick={() => onDropOld(kept)}>
              <Trash2 size={16} aria-hidden="true" />
              {t("replace.dropOld", { old: kept })}
            </button>
          )}
        </div>
      ) : (
        mode === "swap" && (
          <div className="banner banner-ok">
            <span className="banner-text">{t("replace.keptUnknown")}</span>
          </div>
        )
      )}
      {backup && (
        <p className="hint">
          {t("replace.safetyBackup")} <code className="mono">{backup}</code>
        </p>
      )}
    </section>
  );
}

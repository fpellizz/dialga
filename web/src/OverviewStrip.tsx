import { RefreshCw } from "lucide-react";
import { type ReactNode, type RefObject, useEffect, useRef, useState } from "react";
import { errorText, getOverview, type Overview, passwordNeeded } from "./api";
import {
  databasesHref,
  engineLabel,
  formatBytes,
  instanceBadge,
  instanceStatusKey,
  storageShare,
} from "./databases";
import type { EngineInfo } from "./engine";
import { HintText } from "./Feedback";
import { errorHint, hintFor } from "./hints";
import { useI18n } from "./i18n";
import { PasswordNeededBanner, usePassword } from "./PasswordProvider";

/** True once the element has been on screen: off-screen cards cost no AWS calls. */
function useSeen(ref: RefObject<HTMLElement | null>): boolean {
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const element = ref.current;
    if (seen || !element) {
      return;
    }
    if (typeof IntersectionObserver === "undefined") {
      setSeen(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setSeen(true);
      }
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, [ref, seen]);
  return seen;
}

/** A compact state of a target's instance inside its card, loaded when the card shows. */
export function OverviewStrip({ engine, target }: { engine: EngineInfo; target: string }) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const ref = useRef<HTMLDivElement>(null);
  const seen = useSeen(ref);
  const [overview, setOverview] = useState<Overview | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [reload, setReload] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload is the refresh trigger.
  useEffect(() => {
    if (!seen) {
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    getOverview(engine, target, controller.signal).then(
      (o) => {
        if (!controller.signal.aborted) {
          setOverview(o);
          setError(null);
          setLoading(false);
        }
      },
      (e: unknown) => {
        if (!controller.signal.aborted) {
          setError(e);
          setLoading(false);
        }
      },
    );
    return () => controller.abort();
  }, [engine, target, seen, reload, epoch]);

  const refresh = (
    <button
      type="button"
      className="btn btn-ghost btn-sm"
      onClick={() => !loading && setReload((n) => n + 1)}
      aria-disabled={loading || undefined}
      aria-label={t("overview.refresh", { target })}
      title={t("overview.refresh", { target })}
    >
      <RefreshCw size={14} className={loading ? "spin" : undefined} aria-hidden="true" />
    </button>
  );

  return (
    // Busy from the first render: the skeleton shows before the request starts.
    <div ref={ref} className="overview" aria-busy={loading || (!overview && error === null)}>
      {!overview && error === null && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-line" />
          <div className="skeleton skeleton-line" />
        </div>
      )}
      {error !== null && passwordNeeded(error) && (
        <PasswordNeededBanner target={passwordNeeded(error) ?? target} />
      )}
      {error !== null && !passwordNeeded(error) && (
        <div className="overview-row">
          <span className="overview-error">
            <span className="tone-destructive">{errorText(error)}</span>
            <HintText hint={errorHint(error)} className="overview-hint" />
          </span>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => !loading && setReload((n) => n + 1)}
            aria-disabled={loading || undefined}
          >
            {t("overview.retry")}
          </button>
        </div>
      )}
      {overview && error === null && (
        <OverviewBody overview={overview} target={target} refresh={refresh} />
      )}
    </div>
  );
}

function OverviewBody({
  overview: o,
  target,
  refresh,
}: {
  overview: Overview;
  target: string;
  refresh: ReactNode;
}) {
  const { t, lang } = useI18n();
  const statusKey = instanceStatusKey(o.status);
  const share = storageShare(o);
  return (
    <>
      <div className="overview-head">
        <div className="overview-row">
          <span className={`badge ${instanceBadge(o.status)}`}>
            {statusKey ? t(statusKey) : o.status}
          </span>
          <span className="overview-engine" title={`${o.engine} ${o.engineVersion}`}>
            {engineLabel(o.engine, o.engineVersion)}
          </span>
          <span className="mono muted">{o.class}</span>
        </div>
        {refresh}
      </div>

      <div className="overview-storage">
        <div className="overview-row overview-caption">
          <span>{t("overview.storage")}</span>
          {share ? (
            <span className="muted">
              {t("overview.free", {
                free: formatBytes(share.freeBytes, lang),
                total: formatBytes(share.totalBytes, lang),
                percent: share.freePercent,
              })}
            </span>
          ) : (
            <span className="muted" title={o.freeError}>
              {t("overview.freeUnknown")}
            </span>
          )}
        </div>
        {share && (
          <>
            <meter
              className="sr-only"
              min={0}
              max={100}
              value={share.usedPercent}
              aria-label={t("overview.used")}
            />
            <div className="meter" aria-hidden="true">
              <div
                className={`meter-bar meter-${share.tone}`}
                style={{ width: `${share.usedPercent}%` }}
              />
            </div>
          </>
        )}
        {o.maxAllocatedGiB !== undefined && (
          <span className="hint">{t("overview.autoscaling", { max: o.maxAllocatedGiB })}</span>
        )}
      </div>

      {o.sql === "ok" ? (
        <dl className="overview-stats">
          <div>
            <dt>{t("overview.databases")}</dt>
            <dd>
              <a
                href={databasesHref(target)}
                aria-label={`${o.userDatabases ?? 0} · ${t("overview.openDatabases", { target })}`}
              >
                {o.userDatabases ?? 0}
              </a>
            </dd>
          </div>
          <div title={t("overview.activeTasksHint")}>
            <dt>{t("overview.activeTasks")}</dt>
            <dd className={o.activeTasks ? "tone-info" : undefined}>{o.activeTasks ?? 0}</dd>
          </div>
        </dl>
      ) : (
        <details className="overview-sql">
          <summary className={o.sql === "failed" ? "tone-warning" : undefined}>
            {o.sql === "failed" ? t("overview.sqlFailed") : t("overview.sqlSkipped")}
          </summary>
          {o.sqlDetail && <p className="overview-detail">{o.sqlDetail}</p>}
          <HintText hint={hintFor(o.sqlDetail ?? "")} className="overview-hint" />
        </details>
      )}
    </>
  );
}

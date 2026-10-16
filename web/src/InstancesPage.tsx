import {
  Database,
  History,
  LoaderCircle,
  Play,
  Plus,
  RefreshCw,
  TriangleAlert,
  Upload,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { AddInstanceDialog } from "./AddInstanceDialog";
import {
  checkTarget,
  listTargets,
  type StepEvent,
  type TargetList,
  type TargetSummary,
} from "./api";
import type { EngineInfo } from "./engine";
import { ErrorBanner, HintText } from "./Feedback";
import { importHref } from "./ImportPage";
import { useI18n } from "./i18n";
import { OverviewStrip } from "./OverviewStrip";
import { StepList } from "./StepList";
import { TasksPanel } from "./TasksPanel";
import { TunnelIndicator } from "./TunnelIndicator";

interface CheckState {
  running: boolean;
  steps: StepEvent[];
  passed: boolean | null;
  notice: string;
  /** The failed call, null when there is none. */
  error: unknown;
}

const IDLE: CheckState = { running: false, steps: [], passed: null, notice: "", error: null };

/** The configured targets, one card each, with an on-demand check. */
export function InstancesPage({
  engine,
  engineError,
}: {
  engine: EngineInfo | null;
  engineError: string;
}) {
  const { t } = useI18n();
  const [list, setList] = useState<TargetList | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [adding, setAdding] = useState(false);
  const [saved, setSaved] = useState("");
  const savedRef = useRef<HTMLDivElement>(null);

  const load = useCallback(() => {
    if (!engine) {
      return;
    }
    setLoadError(null);
    listTargets(engine).then(setList, setLoadError);
  }, [engine]);

  useEffect(load, [load]);

  // After a save the focus goes to the confirmation: the button that opened
  // the dialog may be gone (the empty state).
  useEffect(() => {
    if (saved) {
      savedRef.current?.focus();
    }
  }, [saved]);

  const openAdd = () => {
    setSaved("");
    setAdding(true);
  };

  const loading = !list && loadError === null && !engineError;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("instances.title")}</h1>
          <p className="lead">{t("instances.lead")}</p>
        </div>
        <div className="head-actions">
          {list && list.targets.length > 0 && (
            <button
              type="button"
              className="btn btn-outline"
              onClick={openAdd}
              disabled={!engine}
              aria-haspopup="dialog"
            >
              <Plus size={16} aria-hidden="true" />
              {t("instances.add")}
            </button>
          )}
          {/* The name starts with the visible text, so voice control finds it (WCAG 2.5.3). */}
          <button
            type="button"
            className="btn btn-outline"
            onClick={load}
            disabled={!engine}
            aria-label={t("instances.refreshList")}
          >
            <RefreshCw size={16} aria-hidden="true" />
            {t("instances.refresh")}
          </button>
        </div>
      </header>

      {saved && (
        <div ref={savedRef} tabIndex={-1} className="banner banner-ok" role="status">
          {t("instances.saved", { name: saved })}
        </div>
      )}

      {engineError ? (
        <ErrorBanner error={engineError} />
      ) : (
        loadError !== null && <ErrorBanner error={loadError} />
      )}
      {loading && (
        <div className="cards" aria-hidden="true">
          <div className="card skeleton" />
          <div className="card skeleton" />
        </div>
      )}
      {list?.error && (
        <div className="banner banner-fail" role="alert">
          <strong>{t("instances.error")}</strong>
          <pre className="error-pre">{list.error}</pre>
          <HintText hint={{ key: "hint.config" }} />
        </div>
      )}
      {list && !list.error && list.targets.length === 0 && (
        <section className="empty" role="status">
          <Database size={20} aria-hidden="true" />
          <h2>{t("instances.empty.title")}</h2>
          <p>{t("instances.empty.hint")}</p>
          <code className="path">{list.configPath}</code>
          <button
            type="button"
            className="btn btn-primary empty-action"
            onClick={openAdd}
            disabled={!engine}
            aria-haspopup="dialog"
          >
            <Plus size={16} aria-hidden="true" />
            {t("instances.add")}
          </button>
        </section>
      )}
      {engine && list && list.targets.length > 0 && (
        <div className="cards">
          {list.targets.map((target) => (
            <TargetCard key={target.name} engine={engine} target={target} />
          ))}
        </div>
      )}
      {adding && engine && (
        <AddInstanceDialog
          engine={engine}
          existing={list?.targets.map((target) => target.name) ?? []}
          onClose={() => setAdding(false)}
          onSaved={(target) => {
            setAdding(false);
            setSaved(target.name);
            load();
          }}
        />
      )}
    </div>
  );
}

function TargetCard({ engine, target }: { engine: EngineInfo; target: TargetSummary }) {
  const { t } = useI18n();
  const [state, setState] = useState<CheckState>(IDLE);
  const [showTasks, setShowTasks] = useState(false);

  let connection = t("connect.tunnel");
  if (target.connectMode === "external") {
    connection = t("connect.external", { port: target.localPort ?? 0 });
  } else if (target.connectMode === "direct") {
    connection = t("connect.direct");
  }

  async function run() {
    if (state.running) {
      return;
    }
    setState({ ...IDLE, running: true });
    const onStep = (event: StepEvent) =>
      setState((s) => {
        const index = s.steps.findIndex((x) => x.step === event.step);
        const steps =
          index === -1 ? [...s.steps, event] : s.steps.map((x, i) => (i === index ? event : x));
        return { ...s, steps };
      });
    const onNotice = (message: string) => setState((s) => ({ ...s, notice: message }));
    try {
      const passed = await checkTarget(engine, target.name, onStep, onNotice);
      setState((s) => ({ ...s, running: false, passed }));
    } catch (e) {
      setState((s) => ({ ...s, running: false, error: e }));
    }
  }

  const failed = state.steps.filter((s) => s.status === "failed").length;
  const warnings = state.steps.filter((s) => s.status === "warning").length;
  let result = "";
  if (state.passed === false) {
    result = t("check.failed");
    if (failed > 0) {
      result += ` · ${t("check.counts", { failed, warnings })}`;
    }
  } else if (state.passed) {
    result = warnings > 0 ? t("check.passedWarnings") : t("check.passed");
  }
  // One polite live region per card, always there: progress and outcome,
  // not every step.
  const announcement = state.running
    ? [t("check.running"), state.notice].filter(Boolean).join(" ")
    : result;

  const titleId = `target-${target.name}`;
  const tasksId = `target-${target.name}-tasks`;
  return (
    <section className="card target" aria-labelledby={titleId}>
      <div className="target-head">
        <div className="tile">
          <Database size={18} aria-hidden="true" />
        </div>
        <div className="target-title">
          <h2 id={titleId} className="mono">
            {target.name}
          </h2>
          <div className="muted">
            {target.instance} · {target.region}
          </div>
        </div>
        {target.protected && <span className="badge badge-warning">{t("target.protected")}</span>}
      </div>

      <dl className="facts">
        <dt>{t("target.account")}</dt>
        <dd className="mono">{target.accountId}</dd>
        <dt>{t("target.profile")}</dt>
        <dd>{target.profile}</dd>
        <dt>{t("target.bucket")}</dt>
        <dd className="mono">{target.bucket}</dd>
        <dt>{t("target.connection")}</dt>
        <dd>{connection}</dd>
      </dl>

      {target.connectMode === "tunnel" && <TunnelIndicator engine={engine} target={target.name} />}

      <OverviewStrip engine={engine} target={target.name} />

      <div className="actions">
        <a
          className="btn btn-outline"
          href={importHref(target.name)}
          aria-label={t("import.into", { target: target.name })}
        >
          <Upload size={16} aria-hidden="true" />
          {t("nav.import")}
        </a>
        <button
          type="button"
          className="btn btn-outline"
          onClick={() => setShowTasks((v) => !v)}
          aria-expanded={showTasks}
          aria-controls={tasksId}
        >
          <History size={16} aria-hidden="true" />
          {showTasks ? t("tasks.hide") : t("tasks.show")}
        </button>
        {/* Busy, not disabled: a disabled button would drop the keyboard focus. */}
        <button
          type="button"
          className="btn btn-primary"
          onClick={run}
          aria-disabled={state.running || undefined}
        >
          {state.running ? (
            <LoaderCircle size={16} className="spin" aria-hidden="true" />
          ) : (
            <Play size={16} aria-hidden="true" />
          )}
          {state.running ? t("check.running") : t("check.run")}
        </button>
      </div>

      <p className="sr-only" role="status">
        {announcement}
      </p>
      {state.notice && (
        <div className="banner banner-warn">
          <TriangleAlert size={16} aria-hidden="true" />
          <span className="banner-text">{state.notice}</span>
        </div>
      )}
      {state.passed !== null && (
        <div className={state.passed ? "banner banner-ok" : "banner banner-fail"}>{result}</div>
      )}
      {state.error !== null && <ErrorBanner error={state.error} />}
      {state.steps.length > 0 && <StepList steps={state.steps} />}
      <div id={tasksId}>{showTasks && <TasksPanel engine={engine} target={target.name} />}</div>
    </section>
  );
}

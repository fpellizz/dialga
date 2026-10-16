import { Database, History, LoaderCircle, Play, RefreshCw, TriangleAlert } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import {
  checkTarget,
  errorText,
  listTargets,
  type StepEvent,
  type TargetList,
  type TargetSummary,
} from "./api";
import type { EngineInfo } from "./engine";
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
  error: string;
}

const IDLE: CheckState = { running: false, steps: [], passed: null, notice: "", error: "" };

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
  const [loadError, setLoadError] = useState("");

  const load = useCallback(() => {
    if (!engine) {
      return;
    }
    setLoadError("");
    listTargets(engine).then(setList, (e: unknown) => setLoadError(errorText(e)));
  }, [engine]);

  useEffect(load, [load]);

  const loading = !list && !loadError && !engineError;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("instances.title")}</h1>
          <p className="lead">{t("instances.lead")}</p>
        </div>
        <button type="button" className="btn btn-outline" onClick={load} disabled={!engine}>
          <RefreshCw size={16} aria-hidden="true" />
          {t("instances.refresh")}
        </button>
      </header>

      {(engineError || loadError) && (
        <div className="banner banner-fail" role="alert">
          {engineError || loadError}
        </div>
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
        </div>
      )}
      {list && !list.error && list.targets.length === 0 && (
        <section className="empty" role="status">
          <Database size={20} aria-hidden="true" />
          <h2>{t("instances.empty.title")}</h2>
          <p>{t("instances.empty.hint")}</p>
          <code className="path">{list.configPath}</code>
        </section>
      )}
      {engine && list && list.targets.length > 0 && (
        <div className="cards">
          {list.targets.map((target) => (
            <TargetCard key={target.name} engine={engine} target={target} />
          ))}
        </div>
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
      setState((s) => ({ ...s, running: false, error: errorText(e) }));
    }
  }

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
        <button type="button" className="btn btn-primary" onClick={run} disabled={state.running}>
          {state.running ? (
            <LoaderCircle size={16} className="spin" aria-hidden="true" />
          ) : (
            <Play size={16} aria-hidden="true" />
          )}
          {state.running ? t("check.running") : t("check.run")}
        </button>
      </div>

      {state.notice && (
        <div className="banner banner-warn" role="status">
          <TriangleAlert size={16} aria-hidden="true" />
          <span className="banner-text">{state.notice}</span>
        </div>
      )}
      {state.passed !== null && (
        <div className={state.passed ? "banner banner-ok" : "banner banner-fail"} role="status">
          {!state.passed
            ? t("check.failed")
            : state.steps.some((s) => s.status === "warning")
              ? t("check.passedWarnings")
              : t("check.passed")}
        </div>
      )}
      {state.error && (
        <div className="banner banner-fail" role="alert">
          {state.error}
        </div>
      )}
      {state.steps.length > 0 && <StepList steps={state.steps} />}
      <div id={tasksId}>{showTasks && <TasksPanel engine={engine} target={target.name} />}</div>
    </section>
  );
}

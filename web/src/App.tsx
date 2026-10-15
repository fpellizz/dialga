import { CircleCheck, CircleMinus, CircleX, LoaderCircle, Play } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { type CheckRequest, runCheck, type StepEvent, type StepStatus } from "./api";
import symbolUrl from "./assets/decisyon-symbol.svg";
import { type EngineInfo, getEngineInfo } from "./engine";

const STORAGE_KEY = "dialga.check.form";
const DEFAULT_FORM: CheckRequest = {
  profile: "",
  region: "us-east-1",
  instance: "",
  localPort: 11433,
  sqlUser: "",
};

function loadForm(): CheckRequest {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    return saved
      ? { ...DEFAULT_FORM, ...(JSON.parse(saved) as Partial<CheckRequest>) }
      : DEFAULT_FORM;
  } catch {
    return DEFAULT_FORM;
  }
}

const STATUS_TEXT: Record<StepStatus, string> = {
  running: "Running",
  ok: "Passed",
  failed: "Failed",
  skipped: "Skipped",
};

function StepIcon({ status }: { status: StepStatus }) {
  switch (status) {
    case "running":
      return <LoaderCircle size={18} className="spin tone-info" aria-hidden="true" />;
    case "ok":
      return <CircleCheck size={18} className="tone-success" aria-hidden="true" />;
    case "failed":
      return <CircleX size={18} className="tone-destructive" aria-hidden="true" />;
    default:
      return <CircleMinus size={18} className="tone-muted" aria-hidden="true" />;
  }
}

export function App() {
  const [engine, setEngine] = useState<EngineInfo | null>(null);
  const [engineError, setEngineError] = useState("");
  const [form, setForm] = useState<CheckRequest>(loadForm);
  const [steps, setSteps] = useState<StepEvent[]>([]);
  const [running, setRunning] = useState(false);
  const [passed, setPassed] = useState<boolean | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    getEngineInfo().then(setEngine, (e: unknown) => setEngineError(String(e)));
  }, []);

  const update = (field: keyof CheckRequest, value: string) =>
    setForm((f) => ({ ...f, [field]: field === "localPort" ? Number(value) : value }));

  const onStep = (event: StepEvent) =>
    setSteps((previous) => {
      const index = previous.findIndex((s) => s.step === event.step);
      if (index === -1) {
        return [...previous, event];
      }
      const next = previous.slice();
      next[index] = event;
      return next;
    });

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!engine) {
      return;
    }
    localStorage.setItem(STORAGE_KEY, JSON.stringify(form));
    setSteps([]);
    setPassed(null);
    setError("");
    setRunning(true);
    try {
      setPassed(await runCheck(engine, form, onStep));
    } catch (e) {
      setError(String(e));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="shell">
      <header className="topbar">
        <img src={symbolUrl} alt="Decisyon" className="logo" />
        <span className="product">Dialga</span>
        <span className="crumb">/ Instance check</span>
        <span className="spacer" />
        {engine ? (
          <span className="engine" title="Local engine">
            <span className="dot" />
            engine {engine.version} · {engine.url.replace("http://", "")}
          </span>
        ) : (
          <span className="engine" title={engineError}>
            <span className={engineError ? "dot dot-off" : "dot dot-wait"} />
            {engineError ? "Engine not available" : "Starting engine…"}
          </span>
        )}
      </header>

      <main className="content">
        <section className="card" aria-labelledby="check-title">
          <h1 id="check-title">Check an instance</h1>
          <p className="lead">
            Read-only checks: AWS credentials, RDS instance, tunnel, SQL Server login and native
            backup tasks.
          </p>
          <form onSubmit={submit}>
            <div className="grid">
              <div className="field">
                <label htmlFor="profile">AWS profile</label>
                <input
                  id="profile"
                  required
                  value={form.profile}
                  onChange={(e) => update("profile", e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="region">Region</label>
                <input
                  id="region"
                  required
                  value={form.region}
                  onChange={(e) => update("region", e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="instance">RDS instance</label>
                <input
                  id="instance"
                  required
                  value={form.instance}
                  onChange={(e) => update("instance", e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="port">Local tunnel port</label>
                <input
                  id="port"
                  type="number"
                  min={1}
                  max={65535}
                  required
                  value={form.localPort}
                  onChange={(e) => update("localPort", e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="login">SQL login</label>
                <input
                  id="login"
                  required
                  value={form.sqlUser}
                  onChange={(e) => update("sqlUser", e.target.value)}
                />
                <span className="hint">The password comes from the engine environment.</span>
              </div>
            </div>
            <div className="actions">
              <button type="submit" className="btn btn-primary" disabled={!engine || running}>
                {running ? (
                  <LoaderCircle size={16} className="spin" aria-hidden="true" />
                ) : (
                  <Play size={16} aria-hidden="true" />
                )}
                {running ? "Checking…" : "Run check"}
              </button>
            </div>
          </form>
        </section>

        {(steps.length > 0 || error) && (
          <section className="card" aria-labelledby="results-title">
            <h2 id="results-title">Results</h2>
            {passed !== null && (
              <div className={passed ? "banner banner-ok" : "banner banner-fail"} role="status">
                {passed ? "All checks passed" : "Some checks failed"}
              </div>
            )}
            {error && (
              <div className="banner banner-fail" role="alert">
                {error}
              </div>
            )}
            <ul className="steps">
              {steps.map((s) => (
                <li key={s.step} className="step">
                  <StepIcon status={s.status} />
                  <div className="step-body">
                    <div className="step-label">{s.label}</div>
                    {s.detail && <div className="step-detail">{s.detail}</div>}
                  </div>
                  <span className={`badge badge-${s.status}`}>{STATUS_TEXT[s.status]}</span>
                </li>
              ))}
            </ul>
          </section>
        )}
      </main>
    </div>
  );
}

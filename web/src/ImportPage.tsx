import { isTauri } from "@tauri-apps/api/core";
import { getCurrentWebview } from "@tauri-apps/api/webview";
import {
  ArrowLeft,
  ArrowRight,
  FileUp,
  LoaderCircle,
  Plus,
  RefreshCw,
  Trash2,
  Upload,
} from "lucide-react";
import { type FormEvent, type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { errorText, listTargets, type StepEvent, type TargetList, type TargetSummary } from "./api";
import { formatBytes } from "./databases";
import type { EngineInfo } from "./engine";
import { ImportPreview, ImportProgress, ImportResult, type RunState } from "./ImportRun";
import { useI18n } from "./i18n";
import {
  addPaths,
  allRestorable,
  baseName,
  type Destination,
  defaultDestinations,
  destinationErrors,
  folderError,
  isAbsolutePath,
  isDifferential,
  parsePaths,
  restoreKey,
  STEPS,
  spaceUse,
  type WizardStep,
} from "./importWizard";
import { type InspectedBackup, type InspectResult, inspectBackups } from "./inspect";
import { StepList } from "./StepList";
import { Wizard } from "./Wizard";

const IMPORT_HASH = "#import/";

/** Link to the Import wizard on a target. */
export function importHref(target: string): string {
  return IMPORT_HASH + encodeURIComponent(target);
}

/** The target of a #import/<target> link, if any. */
export function targetFromImportHash(hash: string): string | null {
  if (!hash.startsWith(IMPORT_HASH)) {
    return null;
  }
  try {
    return decodeURIComponent(hash.slice(IMPORT_HASH.length)) || null;
  } catch {
    return null;
  }
}

interface InspectState {
  /** The files and target it ran on: run again when they change. */
  key: string;
  running: boolean;
  steps: StepEvent[];
  notice: string;
  error: string;
  result: InspectResult | null;
}

const NO_INSPECT: InspectState = {
  key: "",
  running: false,
  steps: [],
  notice: "",
  error: "",
  result: null,
};

const IDLE_RUN: RunState = {
  phase: "idle",
  uploadSteps: [],
  progress: {},
  uploadDone: null,
  uploadError: "",
  restores: [],
};

/**
 * The Import wizard (plan §3.5): files, verification against the target,
 * destination, preview of the upload and of the restores, progress, result.
 * Nothing changes on AWS before "Start import".
 */
export function ImportPage({
  engine,
  engineError,
}: {
  engine: EngineInfo | null;
  engineError: string;
}) {
  const { t } = useI18n();
  const [list, setList] = useState<TargetList | null>(null);
  const [loadError, setLoadError] = useState("");
  const [targetName, setTargetName] = useState(() => targetFromImportHash(window.location.hash));
  const [step, setStep] = useState<WizardStep>("files");
  const [files, setFiles] = useState<string[]>([]);
  const [inspect, setInspect] = useState<InspectState>(NO_INSPECT);
  const [folder, setFolder] = useState<string | null>(null);
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [run, setRun] = useState<RunState>(IDLE_RUN);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const inspectAbort = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!engine) {
      return;
    }
    listTargets(engine).then(setList, (e: unknown) => setLoadError(errorText(e)));
  }, [engine]);

  // The Wizard shell moves the focus to the heading of every new step.
  const show = setStep;

  useEffect(() => () => inspectAbort.current?.abort(), []);

  const targets = list?.targets ?? [];
  const target: TargetSummary | undefined =
    targets.find((x) => x.name === targetName) ?? (targetName ? undefined : targets[0]);
  const running = run.phase === "upload" || run.phase === "restore";
  const inspectKey = target ? `${target.name}\n${files.join("\n")}` : "";
  const s3Folder = folder ?? target?.importPrefix ?? "";

  const chooseTarget = (name: string) => {
    setTargetName(name);
    window.history.replaceState(null, "", importHref(name));
    setStep("files");
    setInspect(NO_INSPECT);
    setFolder(null);
    setRun(IDLE_RUN);
  };

  const runInspect = useCallback(
    async (paths: string[], key: string, name: string) => {
      if (!engine) {
        return;
      }
      inspectAbort.current?.abort();
      const controller = new AbortController();
      inspectAbort.current = controller;
      setInspect({ ...NO_INSPECT, key, running: true });
      const onStep = (event: StepEvent) =>
        setInspect((s) => {
          const index = s.steps.findIndex((x) => x.step === event.step);
          const steps =
            index === -1 ? [...s.steps, event] : s.steps.map((x, i) => (i === index ? event : x));
          return { ...s, steps };
        });
      const onNotice = (notice: string) => setInspect((s) => ({ ...s, notice }));
      try {
        const result = await inspectBackups(
          engine,
          paths,
          name,
          { onStep, onNotice },
          controller.signal,
        );
        if (!controller.signal.aborted) {
          setInspect((s) => ({ ...s, running: false, result }));
          if (result) {
            setDestinations(defaultDestinations(result));
          }
        }
      } catch (e) {
        if (!controller.signal.aborted) {
          setInspect((s) => ({ ...s, running: false, error: errorText(e) }));
        }
      }
    },
    [engine],
  );

  const goTo = (next: WizardStep) => {
    if (next === "verify" && target && inspect.key !== inspectKey) {
      runInspect(files, inspectKey, target.name);
    }
    show(next);
  };

  const removeBackup = (backup: InspectedBackup) => {
    const left = files.filter((f) => !backup.files.includes(f));
    setFiles(left);
    if (target && left.length > 0) {
      runInspect(left, `${target.name}\n${left.join("\n")}`, target.name);
      // The button is gone with its backup: the focus goes back to the step.
      headingRef.current?.focus();
    } else {
      setInspect(NO_INSPECT);
      show("files");
    }
  };

  const reset = () => {
    setFiles([]);
    setInspect(NO_INSPECT);
    setFolder(null);
    setDestinations([]);
    setRun(IDLE_RUN);
    show("files");
  };

  const loading = !list && !loadError && !engineError;
  const picker = (
    <TargetSelect
      targets={targets}
      value={target?.name ?? ""}
      onChange={chooseTarget}
      disabled={running || step === "progress"}
    />
  );

  if (!engine || !target) {
    // No wizard yet: loading, an error, no instance, or an unknown one in the link.
    return (
      <div className="page">
        <header className="page-head">
          <div>
            <h1>{t("import.title")}</h1>
            <p className="lead">{t("import.lead")}</p>
          </div>
        </header>
        {(engineError || loadError || list?.error) && (
          <div className="banner banner-fail" role="alert">
            <span className="banner-text">{engineError || loadError || list?.error}</span>
          </div>
        )}
        {loading && <div className="card skeleton" aria-hidden="true" />}
        {list && !list.error && targets.length === 0 && (
          <section className="empty" role="status">
            <Upload size={20} aria-hidden="true" />
            <h2>{t("import.noTargets")}</h2>
            <p>{t("import.noTargetsHint")}</p>
            <a className="btn btn-primary" href="#instances">
              {t("instances.add")}
            </a>
          </section>
        )}
        {list && targetName && targets.length > 0 && (
          <>
            <div className="banner banner-warn" role="status">
              <span className="banner-text">{t("import.unknownTarget", { name: targetName })}</span>
            </div>
            <div className="card">{picker}</div>
          </>
        )}
      </div>
    );
  }

  return (
    <Wizard
      title={t("import.title")}
      lead={t("import.lead")}
      steps={STEPS.map((id) => ({ id, label: t(`import.step.${id}`) }))}
      current={STEPS.indexOf(step)}
      onStep={(i) => goTo(STEPS[i])}
      locked={running || step === "progress" || step === "result"}
      busy={inspect.running}
      titleRef={headingRef}
    >
      {step === "files" && (
        <FilesStep
          picker={picker}
          files={files}
          onChange={setFiles}
          onNext={() => goTo("verify")}
        />
      )}
      {step !== "files" && (
        <p className="muted">
          {t("import.onTarget", { target: target.name, instance: target.instance })}
        </p>
      )}
      {step === "verify" && (
        <VerifyStep
          inspect={inspect}
          onRetry={() => runInspect(files, inspectKey, target.name)}
          onRemove={removeBackup}
          onBack={() => show("files")}
          onNext={() => goTo("destination")}
        />
      )}
      {step === "destination" && inspect.result && (
        <DestinationStep
          target={target}
          result={inspect.result}
          folder={s3Folder}
          onFolder={setFolder}
          destinations={destinations}
          onDestinations={setDestinations}
          onBack={() => show("verify")}
          onNext={() => goTo("preview")}
        />
      )}
      {step === "preview" && inspect.result && (
        <ImportPreview
          engine={engine}
          target={target}
          result={inspect.result}
          folder={s3Folder}
          destinations={destinations}
          onBack={() => show("destination")}
          onStart={(start) => {
            show("progress");
            start(setRun).then((ok) => ok && show("result"));
          }}
        />
      )}
      {step === "progress" && inspect.result && (
        <ImportProgress
          target={target}
          run={run}
          onBackToPreview={() => {
            setRun(IDLE_RUN);
            show("preview");
          }}
          onResult={() => show("result")}
        />
      )}
      {step === "result" && (
        <ImportResult
          engine={engine}
          target={target}
          destinations={destinations}
          run={run}
          onAgain={reset}
        />
      )}
    </Wizard>
  );
}

/** The instance the import restores on. */
function TargetSelect({
  targets,
  value,
  onChange,
  disabled,
}: {
  targets: TargetSummary[];
  value: string;
  onChange: (name: string) => void;
  disabled: boolean;
}) {
  const { t } = useI18n();
  return (
    <div className="form-field import-target">
      <label htmlFor="import-target">{t("import.target")}</label>
      <select
        id="import-target"
        className="input"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
      >
        {!value && <option value="">{t("import.chooseTarget")}</option>}
        {targets.map((x) => (
          <option key={x.name} value={x.name}>
            {x.name} · {x.instance}
          </option>
        ))}
      </select>
    </div>
  );
}

function WizardNav({
  onBack,
  next,
}: {
  onBack?: () => void;
  next?: { label: string; disabled?: boolean; onClick?: () => void; submit?: boolean };
}) {
  const { t } = useI18n();
  return (
    <div className="wizard-nav">
      {onBack && (
        <button type="button" className="btn btn-outline" onClick={onBack}>
          <ArrowLeft size={16} aria-hidden="true" />
          {t("import.back")}
        </button>
      )}
      {next && (
        <button
          type={next.submit ? "submit" : "button"}
          className="btn btn-primary"
          onClick={next.onClick}
          disabled={next.disabled}
        >
          {next.label}
          <ArrowRight size={16} aria-hidden="true" />
        </button>
      )}
    </div>
  );
}

/** 1. The instance and the local .bak files: dropped on the window (desktop app) or typed. */
function FilesStep({
  picker,
  files,
  onChange,
  onNext,
}: {
  picker: ReactNode;
  files: string[];
  onChange: (files: string[]) => void;
  onNext: () => void;
}) {
  const { t } = useI18n();
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [dragging, setDragging] = useState(false);
  const desktop = isTauri();
  const latest = useRef(files);
  latest.current = files;

  // The desktop shell gives the paths of dropped files (core events only).
  useEffect(() => {
    if (!desktop) {
      return;
    }
    let unlisten: (() => void) | undefined;
    let live = true;
    getCurrentWebview()
      .onDragDropEvent((event) => {
        const payload = event.payload;
        if (payload.type === "enter" || payload.type === "over") {
          setDragging(true);
        } else if (payload.type === "leave") {
          setDragging(false);
        } else if (payload.type === "drop") {
          setDragging(false);
          onChange(addPaths(latest.current, payload.paths));
        }
      })
      .then((stop) => {
        if (live) {
          unlisten = stop;
        } else {
          stop();
        }
      });
    return () => {
      live = false;
      unlisten?.();
    };
  }, [desktop, onChange]);

  const add = (event: FormEvent) => {
    event.preventDefault();
    const paths = parsePaths(text);
    const relative = paths.filter((p) => !isAbsolutePath(p));
    if (paths.length === 0) {
      setError(t("import.error.noPath"));
      return;
    }
    if (relative.length > 0) {
      setError(t("import.error.relative", { path: relative[0] }));
      return;
    }
    onChange(addPaths(files, paths));
    setText("");
    setError("");
  };

  return (
    <div className="wizard-body">
      {picker}
      {/* The browser gives no file paths on a drop: it only blocks navigating to the file. */}
      <section
        className={dragging ? "dropzone dragging" : "dropzone"}
        aria-label={t("import.dropzone")}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => e.preventDefault()}
      >
        <FileUp size={20} aria-hidden="true" />
        <p>{desktop ? t("import.dropHint") : t("import.dropBrowser")}</p>
      </section>

      <form className="form-field" onSubmit={add} noValidate>
        <label htmlFor="import-paths">{t("import.paths")}</label>
        <textarea
          id="import-paths"
          className="input textarea mono"
          rows={3}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setError("");
          }}
          spellCheck={false}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? "import-paths-hint import-paths-error" : "import-paths-hint"}
        />
        <p id="import-paths-hint" className="hint">
          {t("import.pathsHint")}
        </p>
        {error && (
          <p id="import-paths-error" className="field-error">
            {error}
          </p>
        )}
        <div className="actions actions-start">
          <button type="submit" className="btn btn-outline">
            <Plus size={16} aria-hidden="true" />
            {t("import.addPaths")}
          </button>
        </div>
      </form>

      {files.length > 0 && (
        <ul className="item-list" aria-label={t("import.files")}>
          {files.map((path) => (
            <li key={path} className="item">
              <div className="item-row">
                <span className="item-title mono">{baseName(path)}</span>
                <button
                  type="button"
                  className="btn btn-ghost btn-sm"
                  onClick={() => onChange(files.filter((f) => f !== path))}
                  aria-label={t("import.removeFile", { name: baseName(path) })}
                  title={t("import.removeFile", { name: baseName(path) })}
                >
                  <Trash2 size={14} aria-hidden="true" />
                </button>
              </div>
              <div className="mono muted item-detail">{path}</div>
            </li>
          ))}
        </ul>
      )}

      <WizardNav
        next={{ label: t("import.next"), disabled: files.length === 0, onClick: onNext }}
      />
    </div>
  );
}

const BADGE: Record<string, string> = {
  restorable: "badge-ok",
  read: "badge-ok",
  "not-restorable": "badge-failed",
  error: "badge-failed",
};

/** 2. The verdict of each backup against the target, and the space it takes. */
function VerifyStep({
  inspect,
  onRetry,
  onRemove,
  onBack,
  onNext,
}: {
  inspect: InspectState;
  onRetry: () => void;
  onRemove: (backup: InspectedBackup) => void;
  onBack: () => void;
  onNext: () => void;
}) {
  const { t, lang } = useI18n();
  const result = inspect.result;
  const free = result?.target?.freeBytes;
  const space = result ? spaceUse(result.requiredBytes, free) : null;
  return (
    <div className="wizard-body" aria-busy={inspect.running}>
      {inspect.running && (
        <p className="muted" role="status">
          <LoaderCircle size={16} className="spin inline-icon" aria-hidden="true" />
          {t("import.inspecting")}
        </p>
      )}
      {(inspect.running || !result) && inspect.steps.length > 0 && (
        <StepList steps={inspect.steps} />
      )}
      {inspect.error && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{inspect.error}</span>
        </div>
      )}
      {inspect.notice && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{inspect.notice}</span>
        </div>
      )}
      {!inspect.running && (inspect.error || inspect.notice) && (
        <div className="actions actions-start">
          <button type="button" className="btn btn-outline" onClick={onRetry}>
            <RefreshCw size={16} aria-hidden="true" />
            {t("import.retry")}
          </button>
        </div>
      )}

      {result && (
        <>
          <p className="muted">
            {t("import.inspectedWith", { runtime: result.runtime, server: result.server })}
          </p>
          <ul className="item-list">
            {result.backups.map((b) => (
              <li key={b.files.join("\n")} className="item backup">
                <div className="item-row">
                  <span className="item-title mono">{b.files.map(baseName).join(", ")}</span>
                  <span className={`badge ${BADGE[b.outcome] ?? "badge-skipped"}`}>
                    {t(`import.outcome.${b.outcome}`)}
                  </span>
                </div>
                {b.header && (
                  <div className="muted">
                    {t("import.backupFacts", {
                      db: b.header.databaseName,
                      product: b.header.product,
                      version: b.header.version,
                      type: b.header.backupType,
                    })}
                    {" · "}
                    {t("import.needs", { size: formatBytes(b.requiredBytes, lang) })}
                  </div>
                )}
                {b.error && <p className="mono muted item-detail">{b.error}</p>}
                {(b.findings ?? [])
                  .filter((f) => f.status !== "ok")
                  .map((f) => (
                    <p key={f.check} className={f.status === "failed" ? "field-error" : "hint"}>
                      {f.message}
                    </p>
                  ))}
                {b.remedy && (
                  <p className="remedy">
                    <strong>{t("import.remedy")}</strong> {b.remedy}
                  </p>
                )}
                {b.outcome !== "restorable" && (
                  <div className="actions actions-start">
                    <button
                      type="button"
                      className="btn btn-outline btn-sm"
                      onClick={() => onRemove(b)}
                    >
                      <Trash2 size={14} aria-hidden="true" />
                      {t("import.removeBackup")}
                    </button>
                  </div>
                )}
              </li>
            ))}
          </ul>
          {space && free !== undefined && (
            <div className="space">
              <div className="space-label">
                {t("import.space", {
                  required: formatBytes(result.requiredBytes, lang),
                  free: formatBytes(free, lang),
                })}
              </div>
              {/* The label above says the numbers: the bar is decoration. */}
              <div className={`progress space-${space.tone}`} aria-hidden="true">
                <div
                  className="progress-bar"
                  style={{ width: `${Math.min(space.percent, 100)}%` }}
                />
              </div>
            </div>
          )}
          {result.space && result.space.status !== "ok" && (
            <p className={result.space.status === "failed" ? "field-error" : "hint"}>
              {result.space.message}
            </p>
          )}
          {!allRestorable(result) && (
            <div className="banner banner-warn" role="status">
              <span className="banner-text">{t("import.notAllRestorable")}</span>
            </div>
          )}
        </>
      )}

      <WizardNav
        onBack={onBack}
        next={{
          label: t("import.next"),
          disabled: inspect.running || !allRestorable(result),
          onClick: onNext,
        }}
      />
    </div>
  );
}

/** 3. The database name of each backup, its options and the S3 folder. */
function DestinationStep({
  target,
  result,
  folder,
  onFolder,
  destinations,
  onDestinations,
  onBack,
  onNext,
}: {
  target: TargetSummary;
  result: InspectResult;
  folder: string;
  onFolder: (folder: string) => void;
  destinations: Destination[];
  onDestinations: (d: Destination[]) => void;
  onBack: () => void;
  onNext: () => void;
}) {
  const { t } = useI18n();
  const [submitted, setSubmitted] = useState(false);
  const errors = destinationErrors(destinations);
  const folderProblem = folderError(folder);
  const update = (i: number, change: Partial<Destination>) =>
    onDestinations(destinations.map((d, j) => (j === i ? { ...d, ...change } : d)));

  const next = (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (folderProblem) {
      document.getElementById("import-folder")?.focus();
      return;
    }
    const first = errors.findIndex(Boolean);
    if (first !== -1) {
      document.getElementById(`import-db-${first}`)?.focus();
      return;
    }
    onNext();
  };

  return (
    <form className="wizard-body" onSubmit={next} noValidate>
      <div className="form-field">
        <label htmlFor="import-folder">{t("import.folder")}</label>
        <input
          id="import-folder"
          className="input mono"
          value={folder}
          onChange={(e) => onFolder(e.target.value.trim())}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={submitted && folderProblem ? true : undefined}
          aria-describedby={
            submitted && folderProblem
              ? "import-folder-hint import-folder-error"
              : "import-folder-hint"
          }
        />
        <p id="import-folder-hint" className="hint">
          {t("import.folderHint", { bucket: target.bucket, prefix: target.importPrefix })}
        </p>
        {submitted && folderProblem && (
          <p id="import-folder-error" className="field-error">
            {t(folderProblem)}
          </p>
        )}
      </div>

      {result.backups.map((b, i) => {
        const d = destinations[i];
        if (!d) {
          return null;
        }
        const error = submitted ? errors[i] : undefined;
        const id = `import-db-${i}`;
        const differential = isDifferential(b);
        return (
          <fieldset key={b.files.join("\n")} className="form-section destination">
            <legend className="mono">{b.files.map(baseName).join(", ")}</legend>
            <div className="form-field">
              <label htmlFor={id}>{t("import.database")}</label>
              <input
                id={id}
                className="input mono"
                value={d.database}
                onChange={(e) => update(i, { database: e.target.value.trim() })}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${id}-key ${id}-error` : `${id}-key`}
              />
              <p id={`${id}-key`} className="hint mono">
                s3://{target.bucket}/{restoreKey(folder, b.files)}
              </p>
              {error && (
                <p id={`${id}-error`} className="field-error">
                  {t(error)}
                </p>
              )}
            </div>
            {differential && <p className="hint">{t("import.differential", { db: d.database })}</p>}
            <div className="check">
              <input
                id={`${id}-norecovery`}
                type="checkbox"
                checked={d.noRecovery}
                onChange={(e) => update(i, { noRecovery: e.target.checked })}
                aria-describedby={`${id}-norecovery-hint`}
              />
              <div>
                <label htmlFor={`${id}-norecovery`}>{t("import.noRecovery")}</label>
                <p id={`${id}-norecovery-hint`} className="hint">
                  {t("import.noRecoveryHint")}
                </p>
              </div>
            </div>
          </fieldset>
        );
      })}

      <WizardNav onBack={onBack} next={{ label: t("import.toPreview"), submit: true }} />
    </form>
  );
}

import { ArrowLeft, ArrowRight, LoaderCircle, Play, RefreshCw, Square, X } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useRef, useState } from "react";
import {
  type Database,
  getOverview,
  listDatabases,
  listTargets,
  type Overview,
  passwordNeeded,
  type TargetList,
} from "./api";
import {
  DEFAULT_SORT,
  loadLastTarget,
  pickTarget,
  saveLastTarget,
  sortDatabases,
} from "./databases";
import {
  DatabasesStep,
  OptionsStep,
  PreviewStep,
  ProgressStep,
  ResultStep,
  WindowsStep,
} from "./ExportSteps";
import type { EngineInfo } from "./engine";
import { type ExportPreview, previewExport, runExport } from "./exportBackend";
import { cancelJob } from "./exportJob";
import {
  type DownloadOptions,
  exportable,
  exportFromHash,
  exportHref,
  hasErrors,
  type OptionErrors,
  validateOptions,
} from "./exportPlan";
import { type ExportItemRun, tally } from "./exportRun";
import { ErrorBanner, HintText } from "./Feedback";
import { type MessageKey, useI18n } from "./i18n";
import { usePassword } from "./PasswordProvider";
import { Wizard } from "./Wizard";

const STEPS = ["databases", "options", "windows", "preview", "progress", "result"] as const;
type StepId = (typeof STEPS)[number];
const index = (id: StepId) => STEPS.indexOf(id);

/** The databases read for one target. */
interface Loaded {
  target: string;
  list: Database[];
}

/**
 * The Export wizard (#export, #export/<target>?db=…): databases, options
 * (S3 key, download), the windows of the instance, the preview of one plan
 * per database, then the progress and the result. Nothing changes on AWS
 * before "Run export".
 */
export function ExportPage({
  engine,
  engineError,
}: {
  engine: EngineInfo | null;
  engineError: string;
}) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const [step, setStep] = useState<StepId>("databases");
  const [targets, setTargets] = useState<TargetList | null>(null);
  const [targetsError, setTargetsError] = useState<unknown>(null);
  const [target, setTarget] = useState("");
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [dbError, setDbError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string[]>([]);
  // Asked by the address (#export/<target>?db=…): selected once the list arrives.
  const wanted = useRef(exportFromHash(window.location.hash).databases);
  const [keys, setKeys] = useState<Record<string, string>>({});
  const [download, setDownload] = useState<DownloadOptions>({
    enabled: false,
    folder: "",
    overwrite: false,
  });
  const [stepError, setStepError] = useState<MessageKey | null>(null);
  const [optionErrors, setOptionErrors] = useState<OptionErrors>({ keys: {} });
  const [overview, setOverview] = useState<Overview | null>(null);
  const [overviewError, setOverviewError] = useState<unknown>(null);
  const [preview, setPreview] = useState<ExportPreview | null>(null);
  const [previewError, setPreviewError] = useState<unknown>(null);
  const [runError, setRunError] = useState<unknown>(null);
  // The engine job of the export, when the engine runs it as one (Activity).
  const [jobId, setJobId] = useState<string | null>(null);
  const [typed, setTyped] = useState("");
  const [items, setItems] = useState<ExportItemRun[] | null>(null);
  const [running, setRunning] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const abort = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!engine) {
      return;
    }
    let live = true;
    listTargets(engine).then(
      (list) => {
        if (!live) {
          return;
        }
        const names = list.targets.map((x) => x.name);
        setTargets(list);
        setTarget(pickTarget(names, loadLastTarget(), exportFromHash(window.location.hash).target));
      },
      (e: unknown) => live && setTargetsError(e),
    );
    return () => {
      live = false;
    };
  }, [engine]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: epoch is the refresh trigger.
  useEffect(() => {
    if (!engine || !target) {
      return;
    }
    const controller = new AbortController();
    setDbError(null);
    listDatabases(engine, target, controller.signal).then(
      (list) => {
        if (controller.signal.aborted) {
          return;
        }
        setLoaded({ target, list });
        // In the order of the list by name, whatever the order of the address.
        const asked = new Set(wanted.current);
        setSelected(
          sortDatabases(list.filter(exportable), DEFAULT_SORT)
            .map((d) => d.name)
            .filter((name) => asked.has(name)),
        );
        wanted.current = [];
      },
      (e: unknown) => !controller.signal.aborted && setDbError(e),
    );
    return () => controller.abort();
  }, [engine, target, epoch]);

  // Stops following on leave: the RDS tasks go on.
  useEffect(() => () => abort.current?.abort(), []);

  const list = loaded?.target === target ? loaded.list : null;
  // In the order of the selection (by name): the order of the runs too.
  const chosen = selected.flatMap((name) => (list ?? []).filter((d) => d.name === name));

  const choose = (name: string) => {
    saveLastTarget(name);
    setTarget(name);
    setSelected([]);
    setKeys({});
    setPreview(null);
    window.history.replaceState(null, "", exportHref(name));
  };

  const readOverview = useCallback(async () => {
    if (!engine || !target) {
      return;
    }
    setOverview(null);
    setOverviewError(null);
    try {
      setOverview(await getOverview(engine, target));
    } catch (e) {
      setOverviewError(e);
    }
  }, [engine, target]);

  const runPreviews = useCallback(async () => {
    if (!engine || !target) {
      return;
    }
    setPreview(null);
    setPreviewError(null);
    setTyped("");
    try {
      const result = await previewExport(
        engine,
        target,
        selected.map((database) => ({ database, key: keys[database] ?? "" })),
        download.enabled
          ? { folder: download.folder.trim(), overwrite: download.overwrite }
          : undefined,
        targets?.targets.find((x) => x.name === target)?.bucket,
      );
      setPreview(result);
      const ready = result.items.filter((i) => i.ready).length;
      setAnnouncement(t("export.announce.preview", { ready, total: result.items.length }));
    } catch (e) {
      setPreviewError(e);
    }
  }, [engine, target, selected, keys, download, targets, t]);

  // The SQL password was saved after a step asked for it: that step runs again.
  const seenEpoch = useRef(epoch);
  // biome-ignore lint/correctness/useExhaustiveDependencies: epoch is the trigger.
  useEffect(() => {
    if (epoch === seenEpoch.current) {
      return;
    }
    seenEpoch.current = epoch;
    if (step === "windows" && passwordNeeded(overviewError)) {
      void readOverview();
    } else if (step === "preview" && passwordNeeded(previewError)) {
      void runPreviews();
    }
  }, [epoch]);

  function goTo(next: StepId) {
    setStepError(null);
    setStep(next);
    if (next === "windows") {
      void readOverview();
    } else if (next === "preview") {
      void runPreviews();
    }
  }

  function next() {
    if (step === "databases") {
      if (selected.length === 0) {
        setStepError("export.error.none");
        document.querySelector<HTMLInputElement>(".export-db input")?.focus();
        return;
      }
      goTo("options");
    } else if (step === "options") {
      const errors = validateOptions(selected, keys, download);
      setOptionErrors(errors);
      if (hasErrors(errors)) {
        setStepError("export.error.fix");
        // After the render that marks the fields.
        setTimeout(() => document.querySelector<HTMLElement>("[aria-invalid=true]")?.focus(), 0);
        return;
      }
      goTo("windows");
    } else if (step === "windows") {
      goTo("preview");
    }
  }

  const confirmation = preview?.ready ? preview.confirmation : undefined;
  const runnable = !!preview?.ready && (!confirmation || typed === confirmation);

  async function run() {
    if (!engine || !preview || !runnable || running) {
      return;
    }
    const controller = new AbortController();
    abort.current = controller;
    setRunning(true);
    setRunError(null);
    setJobId(null);
    setItems(null);
    setStep("progress");
    let last = "";
    let started = false;
    const onChange = (next: ExportItemRun[]) => {
      started = true;
      setItems(next);
      // Announce each database when it starts and when it ends, not every percent.
      const text = next.map((i) => `${i.database}:${i.state}:${i.step ?? ""}`).join("|");
      if (text !== last) {
        last = text;
        const changed = next.find((i) => i.state === "running");
        if (changed) {
          setAnnouncement(
            t(changed.step === "download" ? "export.announce.download" : "export.announce.backup", {
              database: changed.database,
            }),
          );
        }
      }
    };
    try {
      const result = await runExport(
        engine,
        preview,
        confirmation ? typed : undefined,
        onChange,
        controller.signal,
        setJobId,
      );
      setItems(result);
      setAnnouncement(t("export.announce.done", tally(result)));
      setStep("result");
    } catch (e) {
      if (controller.signal.aborted) {
        // Stopped following a job: it goes on in the engine (Activity).
        setStep("result");
      } else {
        setRunError(e);
        setAnnouncement(t("export.announce.failed"));
        // Refused before anything started (412, 422): back to the preview;
        // 412 means the plan changed or a check failed: preview it again.
        setStep(started ? "result" : "preview");
        if (!started && (e as { status?: unknown } | null)?.status === 412) {
          void runPreviews();
        }
      }
    } finally {
      if (abort.current === controller) {
        abort.current = null;
      }
      setRunning(false);
    }
  }

  async function cancel() {
    if (engine && jobId) {
      try {
        await cancelJob(engine, jobId);
      } catch (e) {
        setRunError(e);
      }
    }
  }

  function startOver() {
    setItems(null);
    setPreview(null);
    setRunError(null);
    setJobId(null);
    setTyped("");
    setSelected([]);
    setKeys({});
    goTo("databases");
  }

  const pageError = engineError || targetsError;
  const steps = STEPS.map((id) => ({ id, label: t(`export.step.${id}`) }));
  const current = index(step);

  let footer: ReactNode = null;
  const back = (to: StepId) => (
    <button type="button" className="btn btn-outline" onClick={() => goTo(to)}>
      <ArrowLeft size={16} aria-hidden="true" />
      {t("export.back")}
    </button>
  );
  const nextButton = (
    <button type="button" className="btn btn-primary" onClick={next} disabled={!list}>
      {t("export.next")}
      <ArrowRight size={16} aria-hidden="true" />
    </button>
  );
  switch (step) {
    case "databases":
      footer = nextButton;
      break;
    case "options":
      footer = (
        <>
          {back("databases")}
          {nextButton}
        </>
      );
      break;
    case "windows":
      footer = (
        <>
          {back("options")}
          {nextButton}
        </>
      );
      break;
    case "preview":
      footer = (
        <>
          {back("windows")}
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() =>
              preview !== null || previewError !== null ? void runPreviews() : undefined
            }
            aria-disabled={(preview === null && previewError === null) || undefined}
          >
            <RefreshCw size={16} aria-hidden="true" />
            {t("export.preview.again")}
          </button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => void run()}
            disabled={!runnable}
          >
            <Play size={16} aria-hidden="true" />
            {t("export.run", { count: selected.length })}
          </button>
        </>
      );
      break;
    case "progress":
      footer = running && (
        <>
          {jobId && (
            <button type="button" className="btn btn-ghost" onClick={() => void cancel()}>
              <X size={16} aria-hidden="true" />
              {t("export.progress.cancel")}
            </button>
          )}
          <button type="button" className="btn btn-outline" onClick={() => abort.current?.abort()}>
            <Square size={16} aria-hidden="true" />
            {t("export.progress.stop")}
          </button>
        </>
      );
      break;
    case "result":
      footer = (
        <>
          <a
            className="btn btn-outline"
            href={target ? `#databases/${encodeURIComponent(target)}` : "#databases"}
          >
            {t("export.result.databases")}
          </a>
          <button type="button" className="btn btn-primary" onClick={startOver}>
            {t("export.result.again")}
          </button>
        </>
      );
      break;
  }

  return (
    <Wizard
      title={t("export.title")}
      lead={t("export.lead")}
      steps={steps}
      current={current}
      onStep={(i) => {
        const id = STEPS[i];
        if (id) {
          goTo(id);
        }
      }}
      locked={running || current >= index("progress")}
      busy={
        (step === "databases" && !list && dbError === null) ||
        (step === "windows" && overview === null && overviewError === null) ||
        (step === "preview" && preview === null && previewError === null) ||
        running
      }
      footer={footer}
    >
      {pageError !== null && pageError !== "" && <ErrorBanner error={pageError} />}
      {targets?.error && (
        <div className="banner banner-fail" role="alert">
          <strong>{t("instances.error")}</strong>
          <pre className="error-pre">{targets.error}</pre>
          <HintText hint={{ key: "hint.config" }} />
        </div>
      )}
      {stepError && (
        <p className="field-error" role="alert">
          {t(stepError)}
        </p>
      )}

      {step === "databases" && (
        <DatabasesStep
          targets={targets}
          target={target}
          onTarget={choose}
          list={list}
          error={dbError}
          selected={selected}
          onSelect={(next) => {
            setSelected(next);
            setStepError(null);
          }}
        />
      )}
      {step === "options" && (
        <OptionsStep
          databases={chosen}
          keys={keys}
          onKeys={setKeys}
          download={download}
          onDownload={setDownload}
          errors={optionErrors}
        />
      )}
      {step === "windows" && (
        <WindowsStep
          overview={overview}
          error={overviewError}
          databases={chosen}
          onRetry={() => void readOverview()}
        />
      )}
      {step === "preview" && previewError !== null && <ErrorBanner error={previewError} />}
      {step === "preview" && runError !== null && <ErrorBanner error={runError} />}
      {step === "preview" && preview === null && previewError === null && (
        <p className="plan-loading">
          <LoaderCircle size={16} className="spin tone-info" aria-hidden="true" />
          {t("export.preview.loading", { count: selected.length })}
        </p>
      )}
      {step === "preview" && preview && (
        <PreviewStep preview={preview} typed={typed} onTyped={setTyped} onRun={() => void run()} />
      )}
      {step === "progress" && <ProgressStep items={items ?? []} running={running} jobId={jobId} />}
      {step === "result" && (
        <ResultStep
          items={items ?? []}
          target={target}
          jobId={jobId}
          stopped={
            !running &&
            items !== null &&
            items.some((i) => i.state === "running" || i.state === "queued")
          }
          error={runError}
        />
      )}

      <div className="sr-only" role="status" aria-live="polite">
        {announcement}
      </div>
    </Wizard>
  );
}

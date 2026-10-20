import { useEffect, useState } from "react";
import { errorText, listTargets, type UpdateInfo } from "./api";
import type { EngineInfo } from "./engine";
import { HintText } from "./Feedback";
import { hintFor } from "./hints";
import { type Lang, type Translate, useI18n } from "./i18n";
import { PrerequisitesCard } from "./PrerequisitesCard";
import { TargetsCard } from "./TargetsCard";
import { THEME_PREFS, type ThemePref } from "./theme";
import { useUpdates } from "./UpdateProvider";

/** Language names in their own language: the same in every UI language. */
const LANGUAGES: { value: Lang; label: string }[] = [
  { value: "en", label: "English" },
  { value: "it", label: "Italiano" },
];

export function Segmented<T extends string>({
  legend,
  name,
  value,
  options,
  onChange,
}: {
  legend: string;
  name: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (value: T) => void;
}) {
  return (
    <fieldset className="field">
      <legend>{legend}</legend>
      <div className="segmented">
        {options.map((o) => (
          <label key={o.value} className="segment">
            <input
              type="radio"
              name={name}
              value={o.value}
              checked={value === o.value}
              onChange={() => onChange(o.value)}
            />
            <span>{o.label}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** What "Check now" found, in a sentence (null: the engine call itself failed). */
function checkResult(info: UpdateInfo | null, t: Translate): string {
  if (!info) {
    return t("update.failed");
  }
  if (info.error) {
    return t("update.failedWith", { error: info.error });
  }
  if (info.newer && info.latest) {
    return t("update.available", { version: info.latest });
  }
  return info.latest ? t("update.upToDate") : t("update.none");
}

/** The "Check for updates" setting, when it last ran and a button to run it now. */
function UpdatesCard() {
  const { t, lang } = useI18n();
  const { enabled, setEnabled, info, checking, checkNow } = useUpdates();
  // Said once, by the click that asked for it (the status region is polite).
  const [result, setResult] = useState("");
  const checked = info?.checkedAt ? new Date(info.checkedAt) : null;
  const when =
    checked && !Number.isNaN(checked.getTime())
      ? t("update.lastChecked", { when: checked.toLocaleString(lang) })
      : t("update.neverChecked");
  const run = async () => {
    setResult("");
    setResult(checkResult(await checkNow(), t));
  };
  return (
    <section className="card settings" aria-labelledby="settings-updates">
      <h2 id="settings-updates">{t("settings.updates")}</h2>
      <label className="check">
        <input
          type="checkbox"
          name="update-check"
          checked={enabled}
          onChange={(e) => setEnabled(e.currentTarget.checked)}
        />
        <span>{t("settings.updatesCheck")}</span>
      </label>
      <p className="hint">{t("settings.updatesHint")}</p>
      <div className="update-check">
        <button
          type="button"
          className="btn btn-outline btn-sm"
          disabled={!enabled || checking}
          onClick={() => void run()}
        >
          {checking ? t("update.checking") : t("update.checkNow")}
        </button>
        <span className="hint">{when}</span>
      </div>
      <p role="status" className="hint">
        {result}
      </p>
    </section>
  );
}

/** Preferences of the app and where its configuration lives. */
export function SettingsPage({
  engine,
  theme,
  onThemeChange,
}: {
  engine: EngineInfo | null;
  theme: ThemePref;
  onThemeChange: (pref: ThemePref) => void;
}) {
  const { t, lang, setLang } = useI18n();
  const [configPath, setConfigPath] = useState("");
  const [configError, setConfigError] = useState("");

  useEffect(() => {
    if (!engine) {
      return;
    }
    let live = true;
    listTargets(engine).then(
      (list) => {
        if (live) {
          setConfigPath(list.configPath);
          setConfigError(list.error ?? "");
        }
      },
      (e: unknown) => live && setConfigError(errorText(e)),
    );
    return () => {
      live = false;
    };
  }, [engine]);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("settings.title")}</h1>
          <p className="lead">{t("settings.lead")}</p>
        </div>
      </header>

      <section className="card settings" aria-labelledby="settings-appearance">
        <h2 id="settings-appearance">{t("settings.appearance")}</h2>
        <Segmented
          legend={t("lang.label")}
          name="language"
          value={lang}
          options={LANGUAGES}
          onChange={setLang}
        />
        <Segmented
          legend={t("settings.theme")}
          name="theme"
          value={theme}
          options={THEME_PREFS.map((value) => ({ value, label: t(`theme.${value}`) }))}
          onChange={onThemeChange}
        />
      </section>

      <UpdatesCard />

      <section className="card settings" aria-labelledby="settings-config">
        <h2 id="settings-config">{t("settings.config")}</h2>
        {configPath ? (
          <code className="path">{configPath}</code>
        ) : (
          !configError && <div className="skeleton skeleton-row" aria-hidden="true" />
        )}
        <p className="hint">{t("settings.configHint")}</p>
        {configError && (
          <div className="banner banner-fail" role="alert">
            <strong>{t("instances.error")}</strong>
            <pre className="error-pre">{configError}</pre>
            <HintText hint={hintFor(configError) ?? { key: "hint.config" }} />
          </div>
        )}
      </section>

      <TargetsCard engine={engine} />
      <PrerequisitesCard engine={engine} />
    </div>
  );
}

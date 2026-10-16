import { useEffect, useState } from "react";
import { errorText, listTargets } from "./api";
import type { EngineInfo } from "./engine";
import { HintText } from "./Feedback";
import { hintFor } from "./hints";
import { type Lang, useI18n } from "./i18n";
import { THEME_PREFS, type ThemePref } from "./theme";

/** Language names in their own language: the same in every UI language. */
const LANGUAGES: { value: Lang; label: string }[] = [
  { value: "en", label: "English" },
  { value: "it", label: "Italiano" },
];

function Segmented<T extends string>({
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

      <section className="card settings" aria-labelledby="settings-about">
        <h2 id="settings-about">{t("settings.about")}</h2>
        <dl className="facts">
          <dt>{t("settings.engine")}</dt>
          <dd className="mono">{engine?.version ?? "—"}</dd>
          <dt>{t("settings.api")}</dt>
          <dd className="mono">{engine ? `v1 · ${engine.url}` : "—"}</dd>
        </dl>
      </section>
    </div>
  );
}

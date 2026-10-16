import { createContext, type ReactNode, useContext, useEffect, useMemo, useState } from "react";

const en = {
  "app.tagline": "Import and export via S3",
  "nav.label": "Main navigation",
  "nav.operations": "Operations",
  "nav.instances": "Instances",
  "nav.import": "Import",
  "nav.export": "Export",
  "nav.activity": "Activity",
  "nav.soon": "Soon",
  "lang.label": "Language",
  "engine.starting": "Starting engine…",
  "engine.unavailable": "Engine not available",
  "engine.version": "engine {version}",
  "instances.title": "Instances",
  "instances.lead": "RDS SQL Server instances configured for import and export through S3.",
  "instances.refresh": "Refresh",
  "instances.empty.title": "No instances yet",
  "instances.empty.hint":
    "Create the configuration file at this path, starting from docs/config.example.yaml:",
  "instances.error": "The configuration file has errors",
  "target.account": "Account",
  "target.profile": "Profile",
  "target.bucket": "Bucket",
  "target.connection": "Connection",
  "target.protected": "Protected",
  "connect.external": "Open tunnel on port {port}",
  "connect.direct": "Direct to the endpoint",
  "connect.tunnel": "Integrated tunnel",
  "check.run": "Run check",
  "check.running": "Checking…",
  "check.passed": "All checks passed",
  "check.failed": "Some checks failed",
  "status.running": "Running",
  "status.ok": "Passed",
  "status.failed": "Failed",
  "status.skipped": "Skipped",
  "step.aws": "AWS credentials",
  "step.instance": "RDS instance",
  "step.tunnel": "Connection",
  "step.sql": "SQL Server login (TLS verified)",
  "step.tasks": "Databases and native backup tasks",
  "tasks.show": "Show tasks",
  "tasks.hide": "Hide tasks",
  "tasks.title": "Native backup and restore tasks",
  "tasks.empty": "No native tasks in the last 36 days.",
  "tasks.updated": "Updated {time}",
  "tasks.details": "Details",
  "tasks.polling": "Refreshing every 30 seconds while a task is running.",
  "lifecycle.CREATED": "Queued",
  "lifecycle.IN_PROGRESS": "In progress",
  "lifecycle.SUCCESS": "Succeeded",
  "lifecycle.ERROR": "Failed",
  "lifecycle.CANCEL_REQUESTED": "Cancelling",
  "lifecycle.CANCELLED": "Cancelled",
} as const;

export type MessageKey = keyof typeof en;
export type Lang = "en" | "it";

const it: Record<MessageKey, string> = {
  "app.tagline": "Import ed export via S3",
  "nav.label": "Navigazione principale",
  "nav.operations": "Operazioni",
  "nav.instances": "Istanze",
  "nav.import": "Importa",
  "nav.export": "Esporta",
  "nav.activity": "Attività",
  "nav.soon": "Presto",
  "lang.label": "Lingua",
  "engine.starting": "Avvio dell'engine…",
  "engine.unavailable": "Engine non disponibile",
  "engine.version": "engine {version}",
  "instances.title": "Istanze",
  "instances.lead": "Istanze RDS SQL Server configurate per import ed export tramite S3.",
  "instances.refresh": "Aggiorna",
  "instances.empty.title": "Nessuna istanza",
  "instances.empty.hint":
    "Crea il file di configurazione in questo percorso, partendo da docs/config.example.yaml:",
  "instances.error": "Il file di configurazione contiene errori",
  "target.account": "Account",
  "target.profile": "Profilo",
  "target.bucket": "Bucket",
  "target.connection": "Connessione",
  "target.protected": "Protetta",
  "connect.external": "Tunnel aperto sulla porta {port}",
  "connect.direct": "Diretta all'endpoint",
  "connect.tunnel": "Tunnel integrato",
  "check.run": "Esegui check",
  "check.running": "Verifica in corso…",
  "check.passed": "Tutti i controlli superati",
  "check.failed": "Alcuni controlli non superati",
  "status.running": "In corso",
  "status.ok": "Superato",
  "status.failed": "Fallito",
  "status.skipped": "Saltato",
  "step.aws": "Credenziali AWS",
  "step.instance": "Istanza RDS",
  "step.tunnel": "Connessione",
  "step.sql": "Login SQL Server (TLS verificato)",
  "step.tasks": "Database e task di backup nativi",
  "tasks.show": "Mostra task",
  "tasks.hide": "Nascondi task",
  "tasks.title": "Task nativi di backup e restore",
  "tasks.empty": "Nessun task nativo negli ultimi 36 giorni.",
  "tasks.updated": "Aggiornato {time}",
  "tasks.details": "Dettagli",
  "tasks.polling": "Aggiornamento ogni 30 secondi finché un task è in corso.",
  "lifecycle.CREATED": "In coda",
  "lifecycle.IN_PROGRESS": "In corso",
  "lifecycle.SUCCESS": "Riuscito",
  "lifecycle.ERROR": "Fallito",
  "lifecycle.CANCEL_REQUESTED": "Annullamento",
  "lifecycle.CANCELLED": "Annullato",
};

export const messages: Record<Lang, Record<MessageKey, string>> = { en, it };

const STORAGE_KEY = "dialga.lang";

export type Translate = (key: MessageKey, vars?: Record<string, string | number>) => string;

export function translate(lang: Lang): Translate {
  return (key, vars) => {
    let text: string = messages[lang][key];
    for (const [name, value] of Object.entries(vars ?? {})) {
      text = text.replaceAll(`{${name}}`, String(value));
    }
    return text;
  };
}

/** Message key for an engine step id, when the UI knows it. */
export function stepKey(step: string): MessageKey | undefined {
  const key = `step.${step}`;
  return key in en ? (key as MessageKey) : undefined;
}

interface I18n {
  lang: Lang;
  setLang: (lang: Lang) => void;
  t: Translate;
}

const I18nContext = createContext<I18n | null>(null);

function initialLang(): Lang {
  try {
    return localStorage.getItem(STORAGE_KEY) === "it" ? "it" : "en";
  } catch {
    return "en";
  }
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(initialLang);
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);
  const value = useMemo<I18n>(
    () => ({
      lang,
      t: translate(lang),
      setLang: (next) => {
        setLangState(next);
        try {
          localStorage.setItem(STORAGE_KEY, next);
        } catch {
          // Private mode or blocked storage: the choice lasts for this session.
        }
      },
    }),
    [lang],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18n {
  const value = useContext(I18nContext);
  if (!value) {
    throw new Error("useI18n outside I18nProvider");
  }
  return value;
}

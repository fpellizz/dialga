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
  "nav.settings": "Settings",
  "settings.title": "Settings",
  "settings.lead": "Preferences of this app, saved on this computer.",
  "settings.appearance": "Appearance",
  "settings.theme": "Theme",
  "theme.system": "System",
  "theme.light": "Light",
  "theme.dark": "Dark",
  "settings.config": "Configuration file",
  "settings.configHint":
    "The targets are read from this file at every refresh. Edit it with any text editor: docs/config.example.yaml shows every key.",
  "settings.about": "About",
  "settings.engine": "Engine",
  "settings.api": "Local API",
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
  "check.passedWarnings": "All checks passed, with warnings",
  "check.failed": "Some checks failed",
  "status.running": "Running",
  "status.ok": "Passed",
  "status.warning": "Warning",
  "status.failed": "Failed",
  "status.skipped": "Skipped",
  "step.aws": "AWS credentials",
  "step.instance": "RDS instance",
  "step.option": "Native backup option",
  "step.bucket": "S3 bucket",
  "step.storage": "Storage",
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
  "nav.databases": "Databases",
  "databases.title": "Databases",
  "databases.lead": "User databases of an instance, with their state and size.",
  "databases.target": "Instance",
  "databases.filter": "Filter",
  "databases.filterPlaceholder": "Name, owner, state or recovery model",
  "databases.caption": "User databases on {target}",
  "databases.count": "{count} databases",
  "databases.countOne": "1 database",
  "databases.countFiltered": "{shown} of {count} databases",
  "databases.col.name": "Name",
  "databases.col.state": "State",
  "databases.col.size": "Size",
  "databases.col.compatibility": "Compatibility",
  "databases.col.recovery": "Recovery",
  "databases.col.owner": "Owner",
  "databases.sizeDetail": "Data {data} · log {log}",
  "databases.readOnly": "Read-only",
  "databases.singleUser": "Single user",
  "databases.restrictedUser": "Restricted",
  "databases.noOwner": "No login has the owner SID",
  "databases.empty.title": "No user databases",
  "databases.empty.hint":
    "This instance has only the system databases. Restored databases show up here.",
  "databases.noMatch.title": "No databases match the filter",
  "databases.noMatch.clear": "Clear filter",
  "databases.noTargets.title": "No instances configured",
  "databases.noTargets.hint": "Add a target to the configuration file to see its databases.",
  "databases.noTargets.action": "Go to Instances",
  "databases.errorHint":
    "The list is read with a SQL session: the engine needs the SQL password and a reachable instance (open tunnel).",
  "dbstate.ONLINE": "Online",
  "dbstate.RESTORING": "Restoring",
  "dbstate.RECOVERING": "Recovering",
  "dbstate.RECOVERY_PENDING": "Recovery pending",
  "dbstate.SUSPECT": "Suspect",
  "dbstate.EMERGENCY": "Emergency",
  "dbstate.OFFLINE": "Offline",
  "overview.storage": "Storage",
  "overview.used": "Storage used",
  "overview.free": "{free} free of {total} ({percent}%)",
  "overview.autoscaling": "Autoscaling up to {max} GiB",
  "overview.freeUnknown": "Free space not available from CloudWatch",
  "overview.databases": "User databases",
  "overview.activeTasks": "Active tasks",
  "overview.activeTasksHint": "Native tasks queued, running or being cancelled",
  "overview.sqlSkipped": "Databases and tasks need the SQL password",
  "overview.sqlFailed": "Databases and tasks not read",
  "overview.retry": "Retry",
  "overview.openDatabases": "Show the databases of {target}",
  "rdsstatus.available": "Available",
  "rdsstatus.backing-up": "Backing up",
  "rdsstatus.modifying": "Modifying",
  "rdsstatus.rebooting": "Rebooting",
  "rdsstatus.starting": "Starting",
  "rdsstatus.stopping": "Stopping",
  "rdsstatus.stopped": "Stopped",
  "rdsstatus.maintenance": "Maintenance",
  "rdsstatus.storage-full": "Storage full",
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
  "nav.settings": "Impostazioni",
  "settings.title": "Impostazioni",
  "settings.lead": "Preferenze di questa app, salvate su questo computer.",
  "settings.appearance": "Aspetto",
  "settings.theme": "Tema",
  "theme.system": "Sistema",
  "theme.light": "Chiaro",
  "theme.dark": "Scuro",
  "settings.config": "File di configurazione",
  "settings.configHint":
    "I target si leggono da questo file a ogni aggiornamento. Si modifica con un editor di testo qualsiasi: docs/config.example.yaml mostra tutte le chiavi.",
  "settings.about": "Informazioni",
  "settings.engine": "Engine",
  "settings.api": "API locale",
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
  "check.passedWarnings": "Tutti i controlli superati, con avvisi",
  "check.failed": "Alcuni controlli non superati",
  "status.running": "In corso",
  "status.ok": "Superato",
  "status.warning": "Attenzione",
  "status.failed": "Fallito",
  "status.skipped": "Saltato",
  "step.aws": "Credenziali AWS",
  "step.instance": "Istanza RDS",
  "step.option": "Opzione di backup nativo",
  "step.bucket": "Bucket S3",
  "step.storage": "Spazio di archiviazione",
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
  "nav.databases": "Database",
  "databases.title": "Database",
  "databases.lead": "Database utente di un'istanza, con stato e dimensione.",
  "databases.target": "Istanza",
  "databases.filter": "Filtra",
  "databases.filterPlaceholder": "Nome, owner, stato o recovery model",
  "databases.caption": "Database utente su {target}",
  "databases.count": "{count} database",
  "databases.countOne": "1 database",
  "databases.countFiltered": "{shown} di {count} database",
  "databases.col.name": "Nome",
  "databases.col.state": "Stato",
  "databases.col.size": "Dimensione",
  "databases.col.compatibility": "Compatibilità",
  "databases.col.recovery": "Recovery",
  "databases.col.owner": "Owner",
  "databases.sizeDetail": "Dati {data} · log {log}",
  "databases.readOnly": "Sola lettura",
  "databases.singleUser": "Utente singolo",
  "databases.restrictedUser": "Accesso limitato",
  "databases.noOwner": "Nessun login ha il SID dell'owner",
  "databases.empty.title": "Nessun database utente",
  "databases.empty.hint":
    "L'istanza ha solo i database di sistema. I database ripristinati compaiono qui.",
  "databases.noMatch.title": "Nessun database corrisponde al filtro",
  "databases.noMatch.clear": "Cancella il filtro",
  "databases.noTargets.title": "Nessuna istanza configurata",
  "databases.noTargets.hint":
    "Aggiungi un target al file di configurazione per vederne i database.",
  "databases.noTargets.action": "Vai a Istanze",
  "databases.errorHint":
    "L'elenco si legge con una sessione SQL: all'engine servono la password SQL e un'istanza raggiungibile (tunnel aperto).",
  "dbstate.ONLINE": "Online",
  "dbstate.RESTORING": "In ripristino",
  "dbstate.RECOVERING": "In recupero",
  "dbstate.RECOVERY_PENDING": "Recupero in sospeso",
  "dbstate.SUSPECT": "Sospetto",
  "dbstate.EMERGENCY": "Emergenza",
  "dbstate.OFFLINE": "Offline",
  "overview.storage": "Spazio",
  "overview.used": "Spazio usato",
  "overview.free": "{free} liberi su {total} ({percent}%)",
  "overview.autoscaling": "Autoscaling fino a {max} GiB",
  "overview.freeUnknown": "Spazio libero non disponibile da CloudWatch",
  "overview.databases": "Database utente",
  "overview.activeTasks": "Task attivi",
  "overview.activeTasksHint": "Task nativi in coda, in corso o in annullamento",
  "overview.sqlSkipped": "Per database e task serve la password SQL",
  "overview.sqlFailed": "Database e task non letti",
  "overview.retry": "Riprova",
  "overview.openDatabases": "Mostra i database di {target}",
  "rdsstatus.available": "Disponibile",
  "rdsstatus.backing-up": "Backup in corso",
  "rdsstatus.modifying": "In modifica",
  "rdsstatus.rebooting": "Riavvio",
  "rdsstatus.starting": "Avvio",
  "rdsstatus.stopping": "Arresto",
  "rdsstatus.stopped": "Arrestata",
  "rdsstatus.maintenance": "Manutenzione",
  "rdsstatus.storage-full": "Spazio esaurito",
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

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
    "Add an RDS SQL Server instance to start. Dialga saves it in the configuration file at this path:",
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
  "instances.add": "Add instance",
  "instances.saved": "Target {name} saved. Run check to verify it.",
  "add.title": "Add instance",
  "add.lead":
    "Find a SQL Server instance with an AWS profile of this computer and save it as a target. Nothing changes on AWS.",
  "add.aws": "AWS account",
  "add.profile": "AWS profile",
  "add.profileHint": "From ~/.aws/config and ~/.aws/credentials.",
  "add.noProfiles":
    "No AWS profiles on this computer: create one with aws configure (or aws configure sso), then type its name.",
  "add.region": "Region",
  "add.discover": "Discover instances",
  "add.discovering": "Discovering…",
  "add.account": "Account {account}. SQL Server instances found: {count}.",
  "add.instance": "Instance",
  "add.noInstances": "This profile sees no RDS SQL Server instance in {region}.",
  "add.discoverFirst": "Discover the instances of the profile, then choose one.",
  "add.backupOk": "Native backup option",
  "add.backupMissing": "No native backup option",
  "add.backupMissingHint":
    "The option group has no SQLSERVER_BACKUP_RESTORE: import and export fail until it is added.",
  "add.details": "Target",
  "add.bucket": "S3 bucket",
  "add.bucketHint":
    "The bucket the SQLSERVER_BACKUP_RESTORE option reads. Run check verifies it after saving.",
  "add.sqlUser": "SQL login",
  "add.sqlUserHint": "The master user of the instance, unless you change it.",
  "add.password": "SQL password",
  "add.passwordNote": "Dialga never stores the password, only where to find it.",
  "add.passwordPrompt": "Ask when needed",
  "add.passwordPromptHint":
    "The app reads DIALGA_SQL_PASSWORD from its environment; the CLI asks on the terminal.",
  "add.passwordEnv": "Environment variable",
  "add.envVar": "Variable name",
  "add.connection": "Connection",
  "add.mode.external": "Open tunnel",
  "add.mode.direct": "Direct",
  "add.mode.tunnel": "Integrated tunnel",
  "add.modeHint.external":
    "A tunnel already open on 127.0.0.1, for example with open-dloop-tunnels.sh.",
  "add.modeHint.direct": "The RDS endpoint itself, from inside the VPC.",
  "add.modeHint.tunnel": "Dialga opens the SSM tunnel itself.",
  "add.localPort": "Local port",
  "add.name": "Target name",
  "add.nameHint": "Lower case letters, digits and -.",
  "add.protected": "Protected",
  "add.protectedHint":
    "Every change on the instance asks to retype the target name. Use it for production.",
  "add.replace": "Replace the existing target {name}",
  "add.save": "Save",
  "add.saving": "Saving…",
  "add.cancel": "Cancel",
  "add.invalid": "Check the highlighted fields.",
  "form.error.profile": "Choose an AWS profile.",
  "form.error.region": "Enter a region such as us-east-1.",
  "form.error.instance": "Choose an instance.",
  "form.error.bucket": "Enter a bucket name: lower case letters, digits, dots and -.",
  "form.error.sqlUser": "Enter the SQL login.",
  "form.error.envVar": "Enter a variable name: letters, digits and _, not starting with a digit.",
  "form.error.localPort": "Enter a port from 1 to 65535.",
  "form.error.name": "Use lower case letters, digits and -, starting with a letter or digit.",
  "form.error.exists": "A target has this name: choose another one, or replace it.",
  "settings.prereqs": "Prerequisites",
  "settings.prereqsLead": "Programs Dialga runs on this computer.",
  "settings.recheck": "Check again",
  "prereq.session-manager-plugin": "Session Manager plugin",
  "prereq.container-runtime": "Container runtime",
  "prereq.purpose.session-manager-plugin": "Needed for the SSM tunnels to the instances.",
  "prereq.purpose.container-runtime": "Needed to inspect .bak files: podman or docker.",
  "prereq.found": "Found",
  "prereq.missing": "Missing",
  "prereq.install": "Install on {distro}",
  "settings.targets": "Targets",
  "settings.targetsLead": "The instances in the configuration file.",
  "settings.noTargets": "No targets yet. Add one from the Instances page.",
  "targets.remove": "Remove",
  "targets.removeTitle": "Remove {name}?",
  "targets.removeHint":
    "The target leaves the configuration file; nothing changes on AWS. Type {name} to confirm.",
  "targets.confirmLabel": "Target name",
  "targets.confirmRemove": "Remove target",
  "targets.removed": "Target {name} removed.",
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
    "Aggiungi un'istanza RDS SQL Server per iniziare. Dialga la salva nel file di configurazione in questo percorso:",
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
  "instances.add": "Aggiungi istanza",
  "instances.saved": "Target {name} salvato. Esegui il check per verificarlo.",
  "add.title": "Aggiungi istanza",
  "add.lead":
    "Trova un'istanza SQL Server con un profilo AWS di questo computer e salvala come target. Su AWS non cambia nulla.",
  "add.aws": "Account AWS",
  "add.profile": "Profilo AWS",
  "add.profileHint": "Da ~/.aws/config e ~/.aws/credentials.",
  "add.noProfiles":
    "Nessun profilo AWS su questo computer: creane uno con aws configure (o aws configure sso), poi scrivine il nome.",
  "add.region": "Regione",
  "add.discover": "Cerca le istanze",
  "add.discovering": "Ricerca in corso…",
  "add.account": "Account {account}. Istanze SQL Server trovate: {count}.",
  "add.instance": "Istanza",
  "add.noInstances": "Questo profilo non vede istanze RDS SQL Server in {region}.",
  "add.discoverFirst": "Cerca le istanze del profilo, poi scegline una.",
  "add.backupOk": "Opzione di backup nativo",
  "add.backupMissing": "Senza opzione di backup nativo",
  "add.backupMissingHint":
    "L'option group non ha SQLSERVER_BACKUP_RESTORE: import ed export falliscono finché non viene aggiunta.",
  "add.details": "Target",
  "add.bucket": "Bucket S3",
  "add.bucketHint":
    "Il bucket che legge l'opzione SQLSERVER_BACKUP_RESTORE. Il check lo verifica dopo il salvataggio.",
  "add.sqlUser": "Login SQL",
  "add.sqlUserHint": "Il master user dell'istanza, se non lo cambi.",
  "add.password": "Password SQL",
  "add.passwordNote": "Dialga non salva mai la password, solo dove trovarla.",
  "add.passwordPrompt": "Chiedi quando serve",
  "add.passwordPromptHint":
    "L'app legge DIALGA_SQL_PASSWORD dal suo ambiente; la CLI la chiede sul terminale.",
  "add.passwordEnv": "Variabile d'ambiente",
  "add.envVar": "Nome della variabile",
  "add.connection": "Connessione",
  "add.mode.external": "Tunnel aperto",
  "add.mode.direct": "Diretta",
  "add.mode.tunnel": "Tunnel integrato",
  "add.modeHint.external":
    "Un tunnel già aperto su 127.0.0.1, per esempio con open-dloop-tunnels.sh.",
  "add.modeHint.direct": "L'endpoint RDS stesso, dall'interno della VPC.",
  "add.modeHint.tunnel": "Dialga apre da sé il tunnel SSM.",
  "add.localPort": "Porta locale",
  "add.name": "Nome del target",
  "add.nameHint": "Lettere minuscole, cifre e -.",
  "add.protected": "Protetta",
  "add.protectedHint":
    "Ogni modifica sull'istanza chiede di riscrivere il nome del target. Usala per la produzione.",
  "add.replace": "Sostituisci il target esistente {name}",
  "add.save": "Salva",
  "add.saving": "Salvataggio…",
  "add.cancel": "Annulla",
  "add.invalid": "Controlla i campi evidenziati.",
  "form.error.profile": "Scegli un profilo AWS.",
  "form.error.region": "Inserisci una regione come us-east-1.",
  "form.error.instance": "Scegli un'istanza.",
  "form.error.bucket": "Inserisci il nome di un bucket: lettere minuscole, cifre, punti e -.",
  "form.error.sqlUser": "Inserisci il login SQL.",
  "form.error.envVar":
    "Inserisci il nome di una variabile: lettere, cifre e _, senza una cifra all'inizio.",
  "form.error.localPort": "Inserisci una porta da 1 a 65535.",
  "form.error.name": "Usa lettere minuscole, cifre e -, iniziando con una lettera o una cifra.",
  "form.error.exists":
    "Esiste già un target con questo nome: scegline un altro, oppure sostituiscilo.",
  "settings.prereqs": "Prerequisiti",
  "settings.prereqsLead": "Programmi che Dialga usa su questo computer.",
  "settings.recheck": "Controlla di nuovo",
  "prereq.session-manager-plugin": "Plugin Session Manager",
  "prereq.container-runtime": "Runtime dei container",
  "prereq.purpose.session-manager-plugin": "Serve per i tunnel SSM verso le istanze.",
  "prereq.purpose.container-runtime": "Serve per ispezionare i file .bak: podman o docker.",
  "prereq.found": "Trovato",
  "prereq.missing": "Mancante",
  "prereq.install": "Installazione su {distro}",
  "settings.targets": "Target",
  "settings.targetsLead": "Le istanze nel file di configurazione.",
  "settings.noTargets": "Ancora nessun target. Aggiungine uno dalla pagina Istanze.",
  "targets.remove": "Rimuovi",
  "targets.removeTitle": "Rimuovere {name}?",
  "targets.removeHint":
    "Il target esce dal file di configurazione; su AWS non cambia nulla. Scrivi {name} per confermare.",
  "targets.confirmLabel": "Nome del target",
  "targets.confirmRemove": "Rimuovi il target",
  "targets.removed": "Target {name} rimosso.",
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

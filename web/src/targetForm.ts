import type { AwsProfile, DiscoveredInstance, TargetInput } from "./api";
import type { MessageKey } from "./i18n";

export type ConnectMode = TargetInput["connectMode"];
export type PasswordKind = "prompt" | "env";

/** The integrated tunnel first: the default, and what the user guide recommends. */
export const CONNECT_MODES: ConnectMode[] = ["tunnel", "external", "direct"];
export const DEFAULT_REGION = "us-east-1";
/** The dev-mssql port of open-dloop-tunnels.sh. */
export const DEFAULT_LOCAL_PORT = 11433;

/** The add-instance form: text inputs keep strings, the engine validates again. */
export interface TargetForm {
  profile: string;
  region: string;
  /** From the discovery: the account the profile points to. */
  accountId: string;
  instance: string;
  bucket: string;
  sqlUser: string;
  passwordKind: PasswordKind;
  envVar: string;
  connectMode: ConnectMode;
  localPort: string;
  name: string;
  protected: boolean;
  replace: boolean;
}

export type FormField =
  | "profile"
  | "region"
  | "instance"
  | "bucket"
  | "sqlUser"
  | "envVar"
  | "localPort"
  | "name";

export type FormErrors = Partial<Record<FormField, MessageKey>>;

/** Fields in the order of the form: the first invalid one gets the focus. */
export const FIELD_ORDER: FormField[] = [
  "profile",
  "region",
  "instance",
  "bucket",
  "sqlUser",
  "envVar",
  "localPort",
  "name",
];

// The rules of the engine's config package.
const PATTERNS = {
  name: /^[a-z0-9][a-z0-9-]{0,62}$/,
  profile: /^[A-Za-z0-9._@+-]{1,64}$/,
  region: /^[a-z]{2}(-[a-z]+)+-[0-9]$/,
  bucket: /^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/,
  login: /^[A-Za-z0-9._@-]{1,128}$/,
  env: /^[A-Za-z_][A-Za-z0-9_]*$/,
};

export function emptyForm(): TargetForm {
  return {
    profile: "",
    region: DEFAULT_REGION,
    accountId: "",
    instance: "",
    bucket: "",
    sqlUser: "",
    passwordKind: "prompt",
    envVar: "",
    // Dialga opens the SSM tunnel itself: no script, no port to choose.
    connectMode: "tunnel",
    // Only used if the user picks an already open tunnel (external).
    localPort: String(DEFAULT_LOCAL_PORT),
    name: "",
    protected: false,
    replace: false,
  };
}

/** The profile to start from: "default" when there is one, else the first. */
export function initialProfile(profiles: AwsProfile[]): string {
  return profiles.find((p) => p.name === "default")?.name ?? profiles[0]?.name ?? "";
}

/** The region of the profile in ~/.aws/config, else us-east-1. */
export function regionFor(profiles: AwsProfile[], name: string): string {
  return profiles.find((p) => p.name === name)?.region || DEFAULT_REGION;
}

/** A target name from an instance identifier (the engine's suggestName). */
export function suggestName(identifier: string): string {
  const name = identifier
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return name.slice(0, 63).replace(/-+$/, "");
}

/** The fields the user changed by hand: picking an instance leaves them alone. */
export type EditedFields = ReadonlySet<FormField | "protected">;

/**
 * Picks a discovered instance: the SQL login, the target name and the
 * protection (on for "prod" instances) follow it unless the user set them.
 */
export function applyInstance(
  form: TargetForm,
  instance: DiscoveredInstance,
  accountId: string,
  edited: EditedFields,
): TargetForm {
  return {
    ...form,
    instance: instance.identifier,
    accountId,
    sqlUser: edited.has("sqlUser") ? form.sqlUser : (instance.masterUser ?? ""),
    name: edited.has("name") ? form.name : suggestName(instance.identifier),
    protected: edited.has("protected") ? form.protected : /prod/i.test(instance.identifier),
  };
}

/** The problems of the form, one message per field; existing are the configured names. */
export function validateForm(form: TargetForm, existing: readonly string[]): FormErrors {
  const errors: FormErrors = {};
  if (!PATTERNS.profile.test(form.profile)) {
    errors.profile = "form.error.profile";
  }
  if (!PATTERNS.region.test(form.region)) {
    errors.region = "form.error.region";
  }
  if (form.instance === "" || form.accountId === "") {
    errors.instance = "form.error.instance";
  }
  if (!PATTERNS.bucket.test(form.bucket)) {
    errors.bucket = "form.error.bucket";
  }
  if (!PATTERNS.login.test(form.sqlUser)) {
    errors.sqlUser = "form.error.sqlUser";
  }
  if (form.passwordKind === "env" && !PATTERNS.env.test(form.envVar)) {
    errors.envVar = "form.error.envVar";
  }
  if (form.connectMode === "external" && !isPort(form.localPort)) {
    errors.localPort = "form.error.localPort";
  }
  if (!PATTERNS.name.test(form.name)) {
    errors.name = "form.error.name";
  } else if (existing.includes(form.name) && !form.replace) {
    errors.name = "form.error.exists";
  }
  return errors;
}

/** The first invalid field in the order of the form. */
export function firstInvalid(errors: FormErrors): FormField | undefined {
  return FIELD_ORDER.find((field) => errors[field]);
}

function isPort(value: string): boolean {
  if (!/^[0-9]{1,5}$/.test(value)) {
    return false;
  }
  const port = Number(value);
  return port >= 1 && port <= 65535;
}

/** The request body; the password is only a reference (prompt or env:VAR). */
export function toTargetInput(form: TargetForm): TargetInput {
  const input: TargetInput = {
    name: form.name,
    accountId: form.accountId,
    region: form.region,
    profile: form.profile,
    instance: form.instance,
    bucket: form.bucket,
    sqlUser: form.sqlUser,
    passwordSource: form.passwordKind === "env" ? `env:${form.envVar}` : "prompt",
    connectMode: form.connectMode,
    protected: form.protected,
  };
  if (form.connectMode === "external") {
    input.localPort = Number(form.localPort);
  }
  if (form.replace) {
    input.replace = true;
  }
  return input;
}

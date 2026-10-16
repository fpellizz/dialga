/**
 * What to do about an error. The engine's messages say the cause; when one
 * does not say what to do, the UI adds a short hint, from the HTTP status
 * or from a known part of the message. Messages that already end with an
 * instruction ("…: open the SSM tunnel") get no hint.
 */
import { ApiError, type StepEvent } from "./api";
import type { MessageKey } from "./i18n";

export interface Hint {
  key: MessageKey;
  vars?: Record<string, string>;
}

/** Message patterns, first match wins. */
const RULES: { test: RegExp; hint: (m: RegExpMatchArray) => Hint }[] = [
  {
    test: /failed to fetch|networkerror|load failed|engine did not start/i,
    hint: () => ({ key: "hint.engine" }),
  },
  {
    test: /points to account \d+, expected (\d+)/,
    hint: (m) => ({ key: "hint.account", vars: { account: m[1] ?? "" } }),
  },
  {
    test: /failed to get shared config profile, ([\w.@+-]+)/,
    hint: (m) => ({ key: "hint.profile", vars: { profile: m[1] ?? "" } }),
  },
  {
    test: /expiredtoken|token has expired|refresh cached sso token|sso session|security token included in the request is (expired|invalid)|invalidclienttokenid/i,
    hint: () => ({ key: "hint.login" }),
  },
  {
    test: /failed to retrieve credentials|no valid credential|get credentials|no ec2 imds role/i,
    hint: () => ({ key: "hint.credentials" }),
  },
  {
    test: /environment variable (\w+) is empty/,
    hint: (m) => ({ key: "hint.envVar", vars: { name: m[1] ?? "" } }),
  },
  {
    // 422 of a password the engine reads itself (env:, secretsmanager:): nothing to type here.
    test: /^SQL password: (secret |system keyring)/,
    hint: () => ({ key: "hint.passwordSource" }),
  },
  { test: /login failed/i, hint: () => ({ key: "hint.sqlLogin" }) },
  { test: /x509|certificate/i, hint: () => ({ key: "hint.tls" }) },
  {
    test: /SQL Server on \S+: .*(connection refused|i\/o timeout|no such host|connection reset|EOF)/i,
    hint: () => ({ key: "hint.tunnel" }),
  },
  { test: /DBInstanceNotFound|instance not found/i, hint: () => ({ key: "hint.instance" }) },
  { test: /database not found on the instance/, hint: () => ({ key: "hint.database" }) },
  { test: /bucket is in another region/, hint: () => ({ key: "hint.bucketRegion" }) },
  { test: /bucket not found|NoSuchBucket/, hint: () => ({ key: "hint.bucket" }) },
  {
    test: /access ?denied|not authorized|UnauthorizedOperation/i,
    hint: () => ({ key: "hint.permission" }),
  },
];

/** The hint for an engine message and, when the engine answered, its HTTP status. */
export function hintFor(message: string, status?: number): Hint | undefined {
  for (const rule of RULES) {
    const m = message.match(rule.test);
    if (m) {
      return rule.hint(m);
    }
  }
  switch (status) {
    case 401:
      return { key: "hint.engine" };
    case 404:
      return { key: "hint.unknownTarget" };
    case 409:
      return { key: "hint.config" };
  }
  return undefined;
}

/** The hint for a failed call: an ApiError carries the status. */
export function errorHint(error: unknown): Hint | undefined {
  const message = error instanceof Error ? error.message : String(error);
  return hintFor(message, error instanceof ApiError ? error.status : undefined);
}

/** The hint for a failed check step: its detail first, then what the step checks. */
export function stepHint(step: StepEvent): Hint | undefined {
  if (step.status !== "failed") {
    return undefined;
  }
  const fromDetail = hintFor(step.detail ?? "");
  if (fromDetail) {
    return fromDetail;
  }
  // "modifying · sqlserver-se 14.00…": the instance answered, its state is wrong.
  if (step.step === "instance" && /^[a-z-]+ · /.test(step.detail ?? "")) {
    return { key: "hint.instanceState" };
  }
  if (step.step === "option") {
    return { key: "hint.option" };
  }
  return undefined;
}

/** The hint under a failed SQL session, by how the engine reaches the instance. */
export function sqlSessionHint(connectMode?: "external" | "direct" | "tunnel"): Hint {
  switch (connectMode) {
    case "tunnel":
      return { key: "databases.errorHint.tunnel" };
    case "external":
      return { key: "databases.errorHint.external" };
    case "direct":
      return { key: "databases.errorHint.direct" };
    default:
      return { key: "databases.errorHint" };
  }
}

/** The hint under a refused password: by HTTP status, as the message is SQL Server's or the engine's. */
export function passwordHint(error: unknown): Hint | undefined {
  switch (error instanceof ApiError ? error.status : undefined) {
    case 422:
      return { key: "password.hint.login" };
    case 409:
      return { key: "password.hint.source" };
    case 501:
      return { key: "password.hint.keyring" };
    case 502:
      // AWS or the tunnel: the password was not even tried.
      return errorHint(error) ?? { key: "password.hint.connection" };
    default:
      return errorHint(error);
  }
}

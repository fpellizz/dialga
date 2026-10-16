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
  { test: /login failed/i, hint: () => ({ key: "hint.sqlLogin" }) },
  { test: /x509|certificate/i, hint: () => ({ key: "hint.tls" }) },
  {
    test: /SQL Server on \S+: .*(connection refused|i\/o timeout|no such host|connection reset|EOF)/i,
    hint: () => ({ key: "hint.tunnel" }),
  },
  { test: /DBInstanceNotFound|instance not found/i, hint: () => ({ key: "hint.instance" }) },
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
  return hintFor(message, statusOf(error));
}

/** The HTTP status of an ApiError, or of any error with a numeric status (plans.HttpError). */
function statusOf(error: unknown): number | undefined {
  if (error instanceof ApiError) {
    return error.status;
  }
  const status = (error as { status?: unknown } | null)?.status;
  return error instanceof Error && typeof status === "number" ? status : undefined;
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

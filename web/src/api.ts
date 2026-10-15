import type { EngineInfo } from "./engine";
import { createSseParser } from "./sse";

export type StepStatus = "running" | "ok" | "failed" | "skipped";

export interface StepEvent {
  step: string;
  label: string;
  status: StepStatus;
  detail?: string;
}

/** A configured target as the engine exposes it: no password reference. */
export interface TargetSummary {
  name: string;
  instance: string;
  region: string;
  profile: string;
  accountId: string;
  bucket: string;
  sqlUser: string;
  connectMode: "external" | "direct" | "tunnel";
  localPort?: number;
  protected: boolean;
}

/** GET /api/v1/targets: an empty list with no error means "no configuration yet". */
export interface TargetList {
  configPath: string;
  error?: string;
  targets: TargetSummary[];
}

function authHeaders(engine: EngineInfo): HeadersInit {
  return { Authorization: `Bearer ${engine.token}` };
}

async function failure(response: Response): Promise<Error> {
  return new Error((await response.text()).trim() || `HTTP ${response.status}`);
}

export async function listTargets(engine: EngineInfo): Promise<TargetList> {
  const response = await fetch(`${engine.url}/api/v1/targets`, { headers: authHeaders(engine) });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as TargetList;
}

/**
 * Runs the read-only checks of a configured target, reporting each step as it
 * starts and ends. onNotice receives warnings sent before the steps (e.g. no
 * SQL password in the engine environment).
 */
export async function checkTarget(
  engine: EngineInfo,
  name: string,
  onStep: (event: StepEvent) => void,
  onNotice: (message: string) => void,
  signal?: AbortSignal,
): Promise<boolean> {
  const response = await fetch(`${engine.url}/api/v1/targets/${encodeURIComponent(name)}/checks`, {
    method: "POST",
    headers: authHeaders(engine),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  let passed = false;
  const parse = createSseParser((message) => {
    if (message.event === "step") {
      onStep(JSON.parse(message.data) as StepEvent);
    } else if (message.event === "notice") {
      onNotice((JSON.parse(message.data) as { message: string }).message);
    } else if (message.event === "done") {
      passed = (JSON.parse(message.data) as { ok: boolean }).ok;
    }
  });
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      return passed;
    }
    parse(value);
  }
}

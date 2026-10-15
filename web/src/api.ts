import type { EngineInfo } from "./engine";
import { createSseParser } from "./sse";

export type StepStatus = "running" | "ok" | "failed" | "skipped";

export interface StepEvent {
  step: string;
  label: string;
  status: StepStatus;
  detail?: string;
}

/** Target of a check. The SQL password is never sent: the engine reads it from its environment. */
export interface CheckRequest {
  profile: string;
  region: string;
  instance: string;
  localPort: number;
  sqlUser: string;
}

function authHeaders(engine: EngineInfo): HeadersInit {
  return { Authorization: `Bearer ${engine.token}` };
}

/** Runs the read-only checks and reports each step as it starts and ends. */
export async function runCheck(
  engine: EngineInfo,
  request: CheckRequest,
  onStep: (event: StepEvent) => void,
  signal?: AbortSignal,
): Promise<boolean> {
  const response = await fetch(`${engine.url}/api/v1/checks`, {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify(request),
    signal,
  });
  if (!response.ok || !response.body) {
    throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
  }
  let passed = false;
  const parse = createSseParser((message) => {
    if (message.event === "step") {
      onStep(JSON.parse(message.data) as StepEvent);
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

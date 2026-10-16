import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { build, type PreviewServer, preview } from "vite";

const WEB = fileURLToPath(new URL("..", import.meta.url));
const ENGINE_MODULE = path.resolve(WEB, "..", "engine");

/**
 * Builds the engine (or takes DIALGA_BIN) and starts "dialga serve" on a
 * temporary configuration, then builds the web app for it (VITE_ENGINE_*)
 * and serves it on a free port. The tests reach that server through the
 * dev origin the engine accepts (e2e/fixtures.ts), so a dev server already
 * running on 1420 does not get in the way.
 *
 * For the tests: DIALGA_E2E_WEB is the web server, DIALGA_E2E_CONFIG the
 * configuration file, which does not exist at first. Returns the teardown.
 */
export default async function globalSetup(): Promise<() => Promise<void>> {
  const dir = mkdtempSync(path.join(tmpdir(), "dialga-e2e-"));
  let engine: ChildProcess | undefined;
  let web: PreviewServer | undefined;
  let log = "";

  const teardown = async () => {
    await web?.close();
    if (engine && engine.exitCode === null) {
      // The engine exits when its stdin closes, like when the shell is gone.
      const exited = new Promise((resolve) => engine?.once("exit", resolve));
      engine.stdin?.end();
      const timer = setTimeout(() => engine?.kill("SIGKILL"), 5_000);
      await exited;
      clearTimeout(timer);
    }
    mkdirSync(path.join(WEB, "test-results"), { recursive: true });
    writeFileSync(path.join(WEB, "test-results", "engine.log"), log);
    rmSync(dir, { recursive: true, force: true });
  };

  try {
    const bin = process.env.DIALGA_BIN || path.join(dir, "dialga");
    if (!process.env.DIALGA_BIN) {
      execFileSync("go", ["build", "-o", bin, "./cmd/dialga"], {
        cwd: ENGINE_MODULE,
        stdio: "inherit",
      });
    }

    const config = path.join(dir, "config.yaml");
    const token = randomBytes(32).toString("hex");
    engine = spawn(bin, ["--config", config, "serve"], {
      env: engineEnv(dir),
      stdio: ["pipe", "pipe", "pipe"],
    });
    engine.stderr?.on("data", (chunk: Buffer) => {
      log = (log + chunk.toString()).slice(-64_000);
    });
    // The session token is the first line on stdin; stdin stays open.
    engine.stdin?.write(`${token}\n`);
    const port = await readyPort(engine, () => log);

    // Baked into the build like "pnpm dev" reads them; the token stays in dir.
    process.env.VITE_ENGINE_URL = `http://127.0.0.1:${port}`;
    process.env.VITE_ENGINE_TOKEN = token;
    const outDir = path.join(dir, "dist");
    await build({ root: WEB, logLevel: "warn", build: { outDir, emptyOutDir: true } });
    web = await preview({
      root: WEB,
      logLevel: "warn",
      build: { outDir },
      preview: { host: "127.0.0.1", port: 0, strictPort: true },
    });
    const { port: webPort } = web.httpServer.address() as AddressInfo;

    process.env.DIALGA_E2E_WEB = `http://127.0.0.1:${webPort}`;
    process.env.DIALGA_E2E_CONFIG = config;
    return teardown;
  } catch (error) {
    await teardown();
    throw error;
  }
}

/**
 * The environment of the engine: no AWS credentials, profiles or metadata
 * service, and no DIALGA_SQL_PASSWORD, so nothing leaves this machine and
 * the checks stop at their first AWS step.
 */
function engineEnv(dir: string): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith("AWS_") && !key.startsWith("DIALGA_")) {
      env[key] = value;
    }
  }
  const awsConfig = path.join(dir, "aws-config");
  const awsCredentials = path.join(dir, "aws-credentials");
  writeFileSync(awsConfig, "");
  writeFileSync(awsCredentials, "");
  return {
    ...env,
    HOME: dir,
    // The job journal of the engine under test, never the user's.
    XDG_STATE_HOME: path.join(dir, "state"),
    DIALGA_DEV: "1",
    AWS_CONFIG_FILE: awsConfig,
    AWS_SHARED_CREDENTIALS_FILE: awsCredentials,
    AWS_EC2_METADATA_DISABLED: "true",
    // For the e2e-env target of e2e/config.ts: a placeholder, never checked.
    E2E_SQL_PASSWORD: "e2e-".repeat(4),
  };
}

/** Waits for {"event":"ready","port":N} on the engine's stdout. */
function readyPort(engine: ChildProcess, log: () => string): Promise<number> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`engine not ready in 15 s\n${log()}`)), 15_000);
    engine.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`engine exited with code ${code}\n${log()}`));
    });
    if (!engine.stdout) {
      reject(new Error("engine without stdout"));
      return;
    }
    createInterface({ input: engine.stdout }).on("line", (line) => {
      try {
        const message = JSON.parse(line) as { event?: string; port?: number };
        if (message.event === "ready" && message.port) {
          clearTimeout(timer);
          resolve(message.port);
        }
      } catch {
        // Not the ready line.
      }
    });
  });
}

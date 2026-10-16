import { invoke, isTauri } from "@tauri-apps/api/core";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getEngineInfo } from "./engine";

vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn(), isTauri: vi.fn(() => false) }));

const INFO = { url: "http://127.0.0.1:43210", token: "t".repeat(48), version: "0.1.0" };

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllEnvs();
  vi.mocked(isTauri).mockReturnValue(false);
  vi.mocked(invoke).mockReset();
});

describe("getEngineInfo", () => {
  it("reads the browser dev server settings", async () => {
    vi.stubEnv("VITE_ENGINE_URL", INFO.url);
    vi.stubEnv("VITE_ENGINE_TOKEN", INFO.token);
    await expect(getEngineInfo()).resolves.toEqual({ ...INFO, version: "dev" });
    expect(invoke).not.toHaveBeenCalled();
  });

  it("explains what the browser dev server needs", async () => {
    vi.stubEnv("VITE_ENGINE_URL", INFO.url);
    vi.stubEnv("VITE_ENGINE_TOKEN", "");
    await expect(getEngineInfo()).rejects.toThrow(
      "Set VITE_ENGINE_URL and VITE_ENGINE_TOKEN to use the browser dev server",
    );
  });

  it("asks the desktop shell until the engine is up", async () => {
    vi.useFakeTimers();
    vi.mocked(isTauri).mockReturnValue(true);
    vi.mocked(invoke)
      .mockRejectedValueOnce("engine not ready")
      .mockRejectedValueOnce("engine not ready")
      .mockResolvedValueOnce(INFO);

    const info = getEngineInfo();
    await vi.advanceTimersByTimeAsync(400);
    await expect(info).resolves.toEqual(INFO);
    expect(invoke).toHaveBeenCalledTimes(3);
    expect(invoke).toHaveBeenCalledWith("engine_info");
  });

  it("gives up after about 5 seconds with the last error", async () => {
    vi.useFakeTimers();
    vi.mocked(isTauri).mockReturnValue(true);
    vi.mocked(invoke).mockRejectedValue("sidecar exited with code 1");

    const info = getEngineInfo();
    const outcome = expect(info).rejects.toThrow("sidecar exited with code 1");
    await vi.advanceTimersByTimeAsync(5_000);
    await outcome;
    expect(invoke).toHaveBeenCalledTimes(25);
  });
});

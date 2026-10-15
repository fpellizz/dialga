import { invoke, isTauri } from "@tauri-apps/api/core";

export interface EngineInfo {
  url: string;
  token: string;
  version: string;
}

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * Where the local engine listens and its session token. Inside the desktop
 * shell the Rust side knows them (it started the engine); in the browser dev
 * server they come from VITE_ENGINE_URL / VITE_ENGINE_TOKEN.
 */
export async function getEngineInfo(): Promise<EngineInfo> {
  if (isTauri()) {
    // The engine needs a moment to start: retry for about 5 seconds.
    let lastError = "";
    for (let attempt = 0; attempt < 25; attempt++) {
      try {
        return await invoke<EngineInfo>("engine_info");
      } catch (error) {
        lastError = String(error);
        await delay(200);
      }
    }
    throw new Error(lastError || "The engine did not start");
  }
  const url = import.meta.env.VITE_ENGINE_URL;
  const token = import.meta.env.VITE_ENGINE_TOKEN;
  if (!url || !token) {
    throw new Error("Set VITE_ENGINE_URL and VITE_ENGINE_TOKEN to use the browser dev server");
  }
  return { url, token, version: "dev" };
}

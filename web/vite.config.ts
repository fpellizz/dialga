import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Port 1420 is the devUrl of the Tauri shell (desktop/src-tauri/tauri.conf.json)
// and the only dev origin the engine accepts (DIALGA_DEV=1).
export default defineConfig({
  plugins: [react()],
  clearScreen: false,
  server: { port: 1420, strictPort: true, host: "localhost" },
  build: { target: "es2022", outDir: "dist" },
  test: { environment: "node" },
});

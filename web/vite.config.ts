import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Port 1420 is the devUrl of the Tauri shell (desktop/src-tauri/tauri.conf.json)
// and the only dev origin the engine accepts (DIALGA_DEV=1).
export default defineConfig({
  plugins: [react()],
  clearScreen: false,
  server: { port: 1420, strictPort: true, host: "localhost" },
  build: { target: "es2022", outDir: "dist" },
  test: {
    // *.test.ts: logic in Node; *.test.tsx: components in a DOM (jsdom).
    // The browser tests in e2e/ belong to Playwright (pnpm run e2e).
    projects: [
      {
        extends: true,
        test: { name: "unit", environment: "node", include: ["src/**/*.test.ts"] },
      },
      {
        extends: true,
        test: {
          name: "dom",
          environment: "jsdom",
          include: ["src/**/*.test.tsx"],
          setupFiles: ["src/test/setup-dom.ts"],
        },
      },
    ],
  },
});

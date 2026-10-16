// Setup of the "dom" Vitest project (*.test.tsx, jsdom).
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";
import { takeUnexpected } from "./http";
import { installMatchMedia } from "./media";

beforeEach(() => {
  installMatchMedia(false);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
  window.location.hash = "";
  document.documentElement.removeAttribute("lang");
  delete document.documentElement.dataset.theme;
  const unmatched = takeUnexpected();
  if (unmatched.length > 0) {
    throw new Error(`requests without a mocked route: ${unmatched.join(", ")}`);
  }
});

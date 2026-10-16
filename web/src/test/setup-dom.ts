// Setup of the "dom" Vitest project (*.test.tsx, jsdom).
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";
import { mockFetch, takeUnexpected } from "./http";
import { installMatchMedia } from "./media";

// jsdom has <dialog> but not showModal() and close(): open and close it like a browser.
if (!HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.open = true;
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.open = false;
    this.dispatchEvent(new Event("close"));
  };
}

beforeEach(() => {
  installMatchMedia(false);
  // No test reaches the network: a request without a route fails it (see afterEach).
  mockFetch({});
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

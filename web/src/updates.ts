/** Preferences of the update check: kept in this browser, storage may be missing or throw. */

const CHECK_KEY = "dialga.updates.check";
const DISMISSED_KEY = "dialga.updates.dismissed";

/** On unless the viewer turned it off. */
export function loadCheckPref(): boolean {
  try {
    return localStorage.getItem(CHECK_KEY) !== "0";
  } catch {
    return true;
  }
}

export function saveCheckPref(on: boolean): void {
  try {
    localStorage.setItem(CHECK_KEY, on ? "1" : "0");
  } catch {
    // The choice lasts until the window closes.
  }
}

/** The version whose notice the viewer closed, or "". */
export function loadDismissed(): string {
  try {
    return localStorage.getItem(DISMISSED_KEY) ?? "";
  } catch {
    return "";
  }
}

export function saveDismissed(version: string): void {
  try {
    localStorage.setItem(DISMISSED_KEY, version);
  } catch {
    // The notice comes back at the next start.
  }
}

/** A release the notice and the About page point to. */
export interface Release {
  version: string;
  /** vX.Y.Z, what the desktop shell opens. */
  tag: string;
  /** The GitHub release page, for a plain link in the browser. */
  url: string;
}

const VERSION = /^\d+\.\d+\.\d+$/;

/** The newer release in an engine answer, or null (also for a version that is not X.Y.Z). */
export function releaseOf(
  info: { latest?: string; newer: boolean; url?: string } | null,
): Release | null {
  if (!info?.newer || !info.latest || !info.url || !VERSION.test(info.latest)) {
    return null;
  }
  return { version: info.latest, tag: `v${info.latest}`, url: info.url };
}

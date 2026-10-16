import { describe, expect, it } from "vitest";
import { loadThemePref, resolveTheme, saveThemePref, THEME_PREFS } from "./theme";

describe("theme", () => {
  it("follows the system only for the system preference", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
  });

  it("falls back to system without storage", () => {
    // The test environment has no localStorage, like a blocked one.
    expect(loadThemePref()).toBe("system");
    expect(() => saveThemePref("dark")).not.toThrow();
  });

  it("offers the three preferences", () => {
    expect(THEME_PREFS).toEqual(["system", "light", "dark"]);
  });
});

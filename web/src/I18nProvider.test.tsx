import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider, useI18n } from "./i18n";

let setLang: ReturnType<typeof useI18n>["setLang"] = () => {};

function Probe() {
  const i18n = useI18n();
  setLang = i18n.setLang;
  return (
    <p>
      {i18n.lang}: {i18n.t("nav.instances")} · {i18n.t("engine.version", { version: "1.2.3" })}
    </p>
  );
}

function renderProbe() {
  return render(
    <I18nProvider>
      <Probe />
    </I18nProvider>,
  );
}

describe("I18nProvider", () => {
  it("speaks English by default and says so on <html>", () => {
    renderProbe();
    expect(screen.getByText("en: Instances · engine 1.2.3")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("en");
  });

  it("starts in the saved language", () => {
    localStorage.setItem("dialga.lang", "it");
    renderProbe();
    expect(screen.getByText("it: Istanze · engine 1.2.3")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("it");
  });

  it("ignores a saved language it does not speak", () => {
    localStorage.setItem("dialga.lang", "fr");
    renderProbe();
    expect(screen.getByText("en: Instances · engine 1.2.3")).toBeInTheDocument();
  });

  it("switches language and saves the choice", () => {
    renderProbe();
    act(() => setLang("it"));
    expect(screen.getByText("it: Istanze · engine 1.2.3")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("it");
    expect(localStorage.getItem("dialga.lang")).toBe("it");
  });

  it("keeps working when the storage is blocked", () => {
    const blocked = () => {
      throw new DOMException("The operation is insecure.", "SecurityError");
    };
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(blocked);
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(blocked);
    renderProbe();
    expect(screen.getByText("en: Instances · engine 1.2.3")).toBeInTheDocument();
    act(() => setLang("it"));
    expect(screen.getByText("it: Istanze · engine 1.2.3")).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("it");
  });

  it("refuses useI18n outside the provider", () => {
    // React and jsdom both report the error React rethrows: keep the output clean.
    vi.spyOn(console, "error").mockImplementation(() => {});
    const silence = (event: ErrorEvent) => event.preventDefault();
    window.addEventListener("error", silence);
    try {
      expect(() => render(<Probe />)).toThrow("useI18n outside I18nProvider");
    } finally {
      window.removeEventListener("error", silence);
    }
  });
});

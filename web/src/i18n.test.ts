import { describe, expect, it } from "vitest";
import { messages, stepKey, translate } from "./i18n";

describe("i18n", () => {
  it("has the same keys in every language", () => {
    expect(Object.keys(messages.it).sort()).toEqual(Object.keys(messages.en).sort());
  });

  it("has no empty message", () => {
    for (const lang of ["en", "it"] as const) {
      for (const [key, text] of Object.entries(messages[lang])) {
        expect(text.trim(), `${lang} ${key}`).not.toBe("");
      }
    }
  });

  it("interpolates variables", () => {
    expect(translate("en")("connect.external", { port: 11433 })).toBe("Open tunnel on port 11433");
    expect(translate("it")("connect.external", { port: 11433 })).toBe(
      "Tunnel aperto sulla porta 11433",
    );
  });

  it("maps the engine step ids it knows", () => {
    expect(stepKey("sql")).toBe("step.sql");
    expect(stepKey("unknown")).toBeUndefined();
  });
});

import { describe, expect, it } from "vitest";
import { isActive, type Lifecycle } from "./api";
import { messages, stepKey, translate } from "./i18n";

const LIFECYCLES: Lifecycle[] = [
  "CREATED",
  "IN_PROGRESS",
  "SUCCESS",
  "ERROR",
  "CANCEL_REQUESTED",
  "CANCELLED",
];

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

  it("labels every task lifecycle", () => {
    for (const lifecycle of LIFECYCLES) {
      expect(messages.en).toHaveProperty(`lifecycle.${lifecycle}`);
    }
  });

  it("treats only unfinished tasks as active", () => {
    const active = LIFECYCLES.filter((lifecycle) =>
      isActive({ id: 1, type: "RESTORE_DB", database: "x", percent: 0, lifecycle }),
    );
    expect(active).toEqual(["CREATED", "IN_PROGRESS", "CANCEL_REQUESTED"]);
  });

  it("maps the engine step ids it knows", () => {
    for (const id of ["aws", "instance", "option", "bucket", "storage", "tunnel", "sql", "tasks"]) {
      expect(stepKey(id), id).toBe(`step.${id}`);
    }
    expect(stepKey("unknown")).toBeUndefined();
  });
});

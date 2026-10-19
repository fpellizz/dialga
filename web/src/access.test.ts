import { describe, expect, it } from "vitest";
import { generatePassword, grantComplete, passwordProblem, validLoginName } from "./access";
import { stepKeys, upsertStep } from "./plans";

describe("passwordProblem", () => {
  it("mirrors the policy of the engine", () => {
    expect(passwordProblem("Short1!")).toBe("length");
    expect(passwordProblem("a".repeat(129))).toBe("length");
    expect(passwordProblem("alllowercaseonly")).toBe("classes");
    expect(passwordProblem("lower and 1234567")).toBeNull();
    expect(passwordProblem("Upper-lower-case")).toBeNull();
    expect(passwordProblem("ÀÈÌÒÙ-àèìòù-1234")).toBeNull();
    // 12 runes, not 12 UTF-16 units.
    expect(passwordProblem("Ab1😀😀😀😀😀😀😀😀😀")).toBeNull();
  });
});

describe("generatePassword", () => {
  it("makes 20 characters that meet the policy with all four classes", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 200; i++) {
      const pw = generatePassword();
      seen.add(pw);
      expect(pw).toHaveLength(20);
      expect(passwordProblem(pw)).toBeNull();
      expect(pw).toMatch(/[A-Z]/);
      expect(pw).toMatch(/[a-z]/);
      expect(pw).toMatch(/[0-9]/);
      expect(pw).toMatch(/[^A-Za-z0-9]/);
    }
    expect(seen.size).toBe(200);
  });
});

describe("grant form", () => {
  it("needs a login, a level and, for a new login, a valid name and password", () => {
    expect(grantComplete({ login: "app", level: "read" }, "")).toBe(true);
    expect(grantComplete({ login: "", level: "read" }, "")).toBe(false);
    expect(grantComplete({ login: "app" }, "")).toBe(false);
    expect(grantComplete({ login: "app", level: "read", createLogin: true }, "weak")).toBe(false);
    expect(
      grantComplete({ login: "-bad", level: "read", createLogin: true }, "Str0ng-enough-pw"),
    ).toBe(false);
    expect(
      grantComplete({ login: "app", level: "read", createLogin: true }, "Str0ng-enough-pw"),
    ).toBe(true);
    expect(validLoginName("app.user-1")).toBe(true);
    expect(validLoginName("a b")).toBe(false);
  });
});

describe("steps with a repeated id", () => {
  const running = (label: string) => ({ step: "add-role", label, status: "running" as const });
  it("keeps two add-role steps apart and updates each by its label", () => {
    let steps = upsertStep([], running("Add u to db_datareader"));
    steps = upsertStep(steps, { ...running("Add u to db_datareader"), status: "ok" });
    steps = upsertStep(steps, running("Add u to db_datawriter"));
    expect(steps.map((s) => s.status)).toEqual(["ok", "running"]);
    steps = upsertStep(steps, { ...running("Add u to db_datawriter"), status: "ok" });
    expect(steps.map((s) => [s.label, s.status])).toEqual([
      ["Add u to db_datareader", "ok"],
      ["Add u to db_datawriter", "ok"],
    ]);
    expect(new Set(stepKeys(steps)).size).toBe(2);
  });
});

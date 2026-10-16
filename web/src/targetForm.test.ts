import { describe, expect, it } from "vitest";
import type { DiscoveredInstance } from "./api";
import { messages } from "./i18n";
import {
  applyInstance,
  CONNECT_MODES,
  emptyForm,
  type FormField,
  firstInvalid,
  initialProfile,
  regionFor,
  suggestName,
  type TargetForm,
  toTargetInput,
  validateForm,
} from "./targetForm";

const instance: DiscoveredInstance = {
  identifier: "Prod-SQLServer",
  engine: "sqlserver-se",
  engineVersion: "14.00",
  class: "db.m6i.large",
  status: "available",
  masterUser: "admin",
  backupRestore: true,
};

function filled(): TargetForm {
  return {
    ...applyInstance(
      { ...emptyForm(), profile: "sales" },
      instance,
      "123456789012",
      new Set<FormField>(),
    ),
    bucket: "prod-backups",
  };
}

describe("target form", () => {
  it("suggests a target name like the engine", () => {
    expect(suggestName("Dev-SQLServer")).toBe("dev-sqlserver");
    expect(suggestName("mssql_dev.01")).toBe("mssql-dev-01");
    expect(suggestName("--x--")).toBe("x");
    expect(suggestName("a".repeat(70))).toBe("a".repeat(63));
    expect(suggestName("a-".repeat(40))).toMatch(/^[a-z0-9][a-z0-9-]{0,62}$/);
    expect(suggestName("a-".repeat(40)).endsWith("-")).toBe(false);
  });

  it("starts from the default profile and its region", () => {
    const profiles = [
      { name: "dev" },
      { name: "default" },
      { name: "sales", region: "eu-south-1" },
    ];
    expect(initialProfile(profiles)).toBe("default");
    expect(initialProfile([{ name: "dev" }])).toBe("dev");
    expect(initialProfile([])).toBe("");
    expect(regionFor(profiles, "sales")).toBe("eu-south-1");
    expect(regionFor(profiles, "dev")).toBe("us-east-1");
  });

  it("fills login, name and protection from the instance unless edited", () => {
    const form = filled();
    expect(form.sqlUser).toBe("admin");
    expect(form.name).toBe("prod-sqlserver");
    expect(form.protected).toBe(true);
    const edited = applyInstance(
      { ...form, sqlUser: "god", name: "mine", protected: false },
      { ...instance, identifier: "other", masterUser: "sa" },
      "123456789012",
      new Set<FormField | "protected">(["sqlUser", "name", "protected"]),
    );
    expect(edited).toMatchObject({
      instance: "other",
      sqlUser: "god",
      name: "mine",
      protected: false,
    });
  });

  it("accepts a complete form", () => {
    expect(validateForm(filled(), ["dev-sql"])).toEqual({});
  });

  it("reports each invalid field and focuses the first", () => {
    const errors = validateForm(
      {
        ...emptyForm(),
        profile: "a b",
        bucket: "Bad_Bucket",
        passwordKind: "env",
        envVar: "1X",
        connectMode: "external",
        localPort: "70000",
        name: "Bad",
      },
      [],
    );
    expect(errors).toEqual({
      profile: "form.error.profile",
      instance: "form.error.instance",
      bucket: "form.error.bucket",
      sqlUser: "form.error.sqlUser",
      envVar: "form.error.envVar",
      localPort: "form.error.localPort",
      name: "form.error.name",
    });
    expect(firstInvalid(errors)).toBe("profile");
    expect(firstInvalid({})).toBeUndefined();
    for (const key of Object.values(errors)) {
      expect(messages.en).toHaveProperty(key);
    }
  });

  it("needs replace for a name already configured", () => {
    expect(validateForm(filled(), ["prod-sqlserver"]).name).toBe("form.error.exists");
    expect(validateForm({ ...filled(), replace: true }, ["prod-sqlserver"]).name).toBeUndefined();
  });

  it("starts from the integrated tunnel, listed first", () => {
    expect(emptyForm().connectMode).toBe("tunnel");
    expect(CONNECT_MODES).toEqual(["tunnel", "external", "direct"]);
  });

  it("ignores the port unless the mode is external", () => {
    expect(validateForm({ ...filled(), connectMode: "direct", localPort: "" }, [])).toEqual({});
  });

  it("sends only a password reference", () => {
    expect(toTargetInput(filled())).toEqual({
      name: "prod-sqlserver",
      accountId: "123456789012",
      region: "us-east-1",
      profile: "sales",
      instance: "Prod-SQLServer",
      bucket: "prod-backups",
      sqlUser: "admin",
      passwordSource: "prompt",
      // The integrated tunnel by default: no port, Dialga picks a free one.
      connectMode: "tunnel",
      protected: true,
    });
    expect(toTargetInput({ ...filled(), connectMode: "external" }).localPort).toBe(11433);
    const env = toTargetInput({
      ...filled(),
      passwordKind: "env",
      envVar: "PROD_PW",
      connectMode: "direct",
      replace: true,
    });
    expect(env.passwordSource).toBe("env:PROD_PW");
    expect(env.localPort).toBeUndefined();
    expect(env.replace).toBe(true);
  });
});

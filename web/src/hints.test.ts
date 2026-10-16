import { describe, expect, it } from "vitest";
import { ApiError, type StepEvent } from "./api";
import { errorHint, hintFor, stepHint } from "./hints";
import { messages } from "./i18n";

describe("hintFor", () => {
  it("maps the engine messages that do not say what to do", () => {
    const cases: [string, string, Record<string, string>?][] = [
      ["Failed to fetch", "hint.engine"],
      ["The engine did not start", "hint.engine"],
      [
        "profile other points to account 000000000000, expected 123456789012",
        "hint.account",
        { account: "123456789012" },
      ],
      [
        "failed to get shared config profile, dialga-no-such-profile",
        "hint.profile",
        { profile: "dialga-no-such-profile" },
      ],
      ["operation error STS: GetCallerIdentity, failed to refresh cached SSO token", "hint.login"],
      [
        "operation error STS: GetCallerIdentity, get identity: get credentials: failed to refresh cached credentials, no EC2 IMDS role found",
        "hint.credentials",
      ],
      [
        "SQL password: environment variable DIALGA_DEV_PW is empty",
        "hint.envVar",
        { name: "DIALGA_DEV_PW" },
      ],
      [
        "SQL Server on 127.0.0.1:11435: dial tcp 127.0.0.1:11435: connect: connection refused",
        "hint.tunnel",
      ],
      [
        "SQL Server on 127.0.0.1:11435: login error: mssql: login failed for user 'admin'",
        "hint.sqlLogin",
      ],
      ["x509: certificate is valid for a, not b", "hint.tls"],
      [
        "RDS instance dev: operation error RDS: DBInstanceNotFound: DBInstance dev not found",
        "hint.instance",
      ],
      ["s3://x not reachable: the bucket is in another region", "hint.bucketRegion"],
      ["s3://x not reachable: bucket not found", "hint.bucket"],
      [
        "s3://x not reachable: access denied: the profile cannot read the bucket",
        "hint.permission",
      ],
    ];
    for (const [message, key, vars] of cases) {
      expect(hintFor(message), message).toEqual(vars ? { key, vars } : { key });
    }
  });

  it("leaves alone the messages that already say what to do", () => {
    for (const message of [
      "nothing listening on 127.0.0.1:11433: open the SSM tunnel",
      "sql.password is prompt: set DIALGA_SQL_PASSWORD, or use env:VAR in the configuration",
      "connect.mode tunnel arrives in v0.4: open the tunnel yourself and use mode external",
    ]) {
      expect(hintFor(message), message).toBeUndefined();
    }
  });

  it("falls back on the HTTP status", () => {
    expect(hintFor("unauthorized", 401)).toEqual({ key: "hint.engine" });
    expect(hintFor('unknown target "x" (known: a)', 404)).toEqual({ key: "hint.unknownTarget" });
    expect(hintFor("/home/x/config.yaml: no targets defined", 409)).toEqual({ key: "hint.config" });
    expect(hintFor("something else", 502)).toBeUndefined();
  });

  it("reads the status of an ApiError", () => {
    expect(errorHint(new ApiError(409, "invalid YAML"))).toEqual({ key: "hint.config" });
    expect(errorHint(new TypeError("Failed to fetch"))).toEqual({ key: "hint.engine" });
    expect(errorHint("plain text")).toBeUndefined();
  });

  it("only uses keys that exist in both languages", () => {
    const keys = [
      "hint.engine",
      "hint.config",
      "hint.unknownTarget",
      "hint.account",
      "hint.profile",
      "hint.login",
      "hint.credentials",
      "hint.permission",
      "hint.envVar",
      "hint.tunnel",
      "hint.sqlLogin",
      "hint.tls",
      "hint.instance",
      "hint.instanceState",
      "hint.option",
      "hint.bucket",
      "hint.bucketRegion",
    ];
    for (const key of keys) {
      expect(messages.en, key).toHaveProperty(key);
      expect(messages.it, key).toHaveProperty(key);
    }
  });
});

describe("stepHint", () => {
  const step = (s: Partial<StepEvent>): StepEvent => ({
    step: "aws",
    label: "x",
    status: "failed",
    ...s,
  });

  it("hints only failed steps", () => {
    expect(stepHint(step({ status: "warning", detail: "access denied" }))).toBeUndefined();
    expect(stepHint(step({ detail: "failed to get shared config profile, p" }))?.key).toBe(
      "hint.profile",
    );
  });

  it("knows what the instance and option steps need", () => {
    expect(
      stepHint(
        step({
          step: "instance",
          detail: "modifying · sqlserver-se 14.00 · option group og pending",
        }),
      ),
    ).toEqual({ key: "hint.instanceState" });
    expect(stepHint(step({ step: "instance", detail: "throttled" }))).toBeUndefined();
    expect(
      stepHint(
        step({ step: "option", detail: "SQLSERVER_BACKUP_RESTORE missing in option group og" }),
      ),
    ).toEqual({ key: "hint.option" });
    expect(
      stepHint(step({ step: "tunnel", detail: "nothing listening on x: open the SSM tunnel" })),
    ).toBeUndefined();
  });
});

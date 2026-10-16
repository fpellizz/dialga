import { rm, writeFile } from "node:fs/promises";

/**
 * One target on a fake instance. Its AWS profile does not exist in the
 * engine's (empty) AWS configuration, so a check fails at the AWS step
 * without any network call. Placeholders only.
 */
export const FAKE_TARGET = `apiVersion: v1
targets:
  e2e-fake:
    account_id: "123456789012"
    region: eu-west-1
    aws_profile: dialga-e2e-missing
    instance: e2e-fake-instance
    bucket: e2e-fake-bucket
    sql:
      user: e2e
      password: prompt
    connect:
      mode: external
      local_port: 11433
`;

/**
 * FAKE_TARGET plus e2e-env, whose SQL password is in the engine environment
 * (E2E_SQL_PASSWORD, a placeholder): its SQL session gets as far as the AWS
 * profile and fails there (502), while e2e-fake stops at the password (422).
 */
export const TWO_TARGETS = `${FAKE_TARGET}  e2e-env:
    account_id: "123456789012"
    region: eu-west-1
    aws_profile: dialga-e2e-missing
    instance: e2e-env-instance
    bucket: e2e-fake-bucket
    sql:
      user: e2e
      password: env:E2E_SQL_PASSWORD
    connect:
      mode: external
      local_port: 11434
`;

/** A configuration with an unknown key: the engine reads it strictly. */
export const BROKEN = `apiVersion: v1
targets:
  e2e-fake:
    bogus_key: true
`;

/** The configuration file of the engine under test (e2e/global-setup.ts). */
export function configPath(): string {
  const path = process.env.DIALGA_E2E_CONFIG;
  if (!path) {
    throw new Error("DIALGA_E2E_CONFIG is not set: run the tests with pnpm run e2e");
  }
  return path;
}

/** The engine reads the file on every request: no restart needed. */
export async function writeConfig(text: string): Promise<void> {
  await writeFile(configPath(), text);
}

export async function removeConfig(): Promise<void> {
  await rm(configPath(), { force: true });
}

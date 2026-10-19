/**
 * Database access: who can use a database (read-only views of the engine)
 * and the pure helpers of the grant dialog (the password of a new login).
 * The operations themselves (grant, revoke, fix-orphan) are plans: plans.ts.
 */
import { authHeaders, failure } from "./api";
import type { EngineInfo } from "./engine";
import type { MessageKey } from "./i18n";
import type { AccessLevel } from "./plans";

/** A SQL login of the instance (GET .../logins). */
export interface Login {
  name: string;
  disabled: boolean;
  /** The master user of the target: its access is never changed. */
  master: boolean;
  createdAt?: string;
}

export type UserLevel = "none" | "read" | "readwrite" | "owner" | "custom";

/** A SQL user of a database. */
export interface DatabaseUser {
  name: string;
  /** Absent for an orphan. */
  login?: string;
  orphan: boolean;
  roles: string[];
  level: UserLevel;
  backup: boolean;
  /** Only one half of backup is there (the role or the msdb grants). */
  backupPartial?: boolean;
  /** false: dbo, the master user and the like, shown read-only. */
  managed: boolean;
}

/** GET .../databases/{db}/access. */
export interface DatabaseAccess {
  target: string;
  instance: string;
  database: string;
  users: DatabaseUser[];
  windowsPrincipals: number;
}

function targetURL(engine: EngineInfo, target: string): string {
  return `${engine.url}/api/v1/targets/${encodeURIComponent(target)}`;
}

/** The SQL logins of the instance (read-only). */
export async function listLogins(
  engine: EngineInfo,
  target: string,
  signal?: AbortSignal,
): Promise<Login[]> {
  const response = await fetch(`${targetURL(engine, target)}/logins`, {
    headers: authHeaders(engine),
    signal,
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return ((await response.json()) as { logins: Login[] }).logins;
}

/** Who can use a database, and how (read-only). */
export async function getDatabaseAccess(
  engine: EngineInfo,
  target: string,
  database: string,
  signal?: AbortSignal,
): Promise<DatabaseAccess> {
  const response = await fetch(
    `${targetURL(engine, target)}/databases/${encodeURIComponent(database)}/access`,
    { headers: authHeaders(engine), signal },
  );
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as DatabaseAccess;
}

export const LEVEL_KEY: Record<UserLevel, MessageKey> = {
  none: "access.level.none",
  read: "access.level.read",
  readwrite: "access.level.readwrite",
  owner: "access.level.owner",
  custom: "access.level.custom",
};

/** The levels a grant offers, with the roles each gives. */
export const GRANT_LEVELS: { level: AccessLevel; label: MessageKey; hint: MessageKey }[] = [
  { level: "read", label: "access.level.read", hint: "grant.level.readHint" },
  { level: "readwrite", label: "access.level.readwrite", hint: "grant.level.readwriteHint" },
  { level: "owner", label: "access.level.owner", hint: "grant.level.ownerHint" },
];

/** The name of a login, as the engine accepts it (validate.PrincipalName). */
export function validLoginName(name: string): boolean {
  return /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/.test(name);
}

const PASSWORD_MIN = 12;
const PASSWORD_MAX = 128;

/**
 * Why a password is refused, as the engine checks it (ops.CheckLoginPassword):
 * 12 to 128 characters and at least three of upper case, lower case, digits
 * and symbols. null when it passes. SQL Server checks its own policy too.
 */
export function passwordProblem(password: string): "length" | "classes" | null {
  const length = [...password].length;
  if (length < PASSWORD_MIN || length > PASSWORD_MAX) {
    return "length";
  }
  const classes = [/\p{Lu}/u, /\p{Ll}/u, /\p{Nd}/u, /[^\p{Lu}\p{Ll}\p{Nd}]/u].filter((re) =>
    re.test(password),
  ).length;
  return classes < 3 ? "classes" : null;
}

// Without the look-alikes (0 O 1 l I) and the characters that trip up shells and URLs.
const UPPER = "ABCDEFGHJKLMNPQRSTUVWXYZ";
const LOWER = "abcdefghijkmnopqrstuvwxyz";
const DIGITS = "23456789";
const SYMBOLS = "!@#%^*-_=+?";

/** An integer in [0, max) from crypto.getRandomValues, without modulo bias. */
function randomBelow(max: number): number {
  const limit = Math.floor(0x1_0000_0000 / max) * max;
  const buffer = new Uint32Array(1);
  do {
    crypto.getRandomValues(buffer);
  } while ((buffer[0] ?? 0) >= limit);
  return (buffer[0] ?? 0) % max;
}

/**
 * A random password of 20 characters with all four classes (crypto.getRandomValues),
 * so it meets the policy of the engine whatever SQL Server adds.
 */
export function generatePassword(length = 20): string {
  const all = UPPER + LOWER + DIGITS + SYMBOLS;
  const pick = (set: string) => set.charAt(randomBelow(set.length));
  const chars = [pick(UPPER), pick(LOWER), pick(DIGITS), pick(SYMBOLS)];
  while (chars.length < length) {
    chars.push(pick(all));
  }
  // Fisher-Yates, so the four guaranteed characters are not always first.
  for (let i = chars.length - 1; i > 0; i--) {
    const j = randomBelow(i + 1);
    [chars[i], chars[j]] = [chars[j] as string, chars[i] as string];
  }
  return chars.join("");
}

/** The grant form has what the engine needs: a login, a level, and a password that passes for a new login. */
export function grantComplete(
  request: { login?: string; level?: string; createLogin?: boolean },
  password: string,
): boolean {
  const login = request.login?.trim() ?? "";
  if (!request.level || (request.createLogin ? !validLoginName(login) : login === "")) {
    return false;
  }
  return !request.createLogin || passwordProblem(password) === null;
}

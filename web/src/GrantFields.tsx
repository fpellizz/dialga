import { Check, Copy, Eye, EyeOff, KeyRound } from "lucide-react";
import { useEffect, useId, useState } from "react";
import {
  GRANT_LEVELS,
  generatePassword,
  type Login,
  listLogins,
  passwordProblem,
  validLoginName,
} from "./access";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { usePassword } from "./PasswordProvider";
import { type PlanRequest, planHint } from "./plans";

/**
 * The inputs of a grant: an existing login (a select of the logins of the
 * instance, without the master user) or a new one with its password, the
 * level of access and whether it can back the database up. The password
 * lives in the caller's state, apart from the request: it goes into the
 * execute call only, never into a preview.
 */
export function GrantFields({
  engine,
  target,
  params,
  onChange,
  password,
  onPassword,
  disabled,
}: {
  engine: EngineInfo;
  target: string;
  params: PlanRequest;
  onChange: (patch: Partial<PlanRequest>) => void;
  password: string;
  onPassword: (password: string) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const ids = useId();
  const [logins, setLogins] = useState<Login[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [shown, setShown] = useState(false);
  const [copied, setCopied] = useState(false);
  const creating = !!params.createLogin;
  const login = params.login ?? "";

  // biome-ignore lint/correctness/useExhaustiveDependencies: epoch is the retry trigger (the SQL password was saved).
  useEffect(() => {
    const controller = new AbortController();
    setLoadError(null);
    listLogins(engine, target, controller.signal).then(
      (list) => !controller.signal.aborted && setLogins(list),
      (e: unknown) => !controller.signal.aborted && setLoadError(e),
    );
    return () => controller.abort();
  }, [engine, target, epoch]);

  const problem = password === "" ? null : passwordProblem(password);
  const nameTaken =
    creating && !!logins?.some((l) => l.name.toLowerCase() === login.trim().toLowerCase());
  const nameInvalid = creating && login.trim() !== "" && !validLoginName(login.trim());

  function setMode(createLogin: boolean) {
    onChange({ createLogin, login: "" });
    onPassword("");
    setShown(false);
    setCopied(false);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(password);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return (
    <>
      <fieldset className="form-section grant-fieldset">
        <legend>{t("grant.login")}</legend>
        <div className="check">
          <input
            id={`${ids}-existing`}
            type="radio"
            name={`${ids}-mode`}
            checked={!creating}
            disabled={disabled}
            onChange={() => setMode(false)}
          />
          <label htmlFor={`${ids}-existing`}>{t("grant.existing")}</label>
        </div>
        <div className="check">
          <input
            id={`${ids}-new`}
            type="radio"
            name={`${ids}-mode`}
            checked={creating}
            disabled={disabled}
            onChange={() => setMode(true)}
          />
          <label htmlFor={`${ids}-new`}>{t("grant.new")}</label>
        </div>

        {loadError !== null && <ErrorBanner error={loadError} fallback={planHint(loadError)} />}

        {!creating && (
          <label className="field-inline">
            <span>{t("grant.pick")}</span>
            <select
              className="select mono"
              name="login"
              value={login}
              disabled={disabled || logins === null}
              onChange={(e) => onChange({ login: e.currentTarget.value })}
            >
              <option value="">{logins === null ? t("grant.loading") : t("grant.choose")}</option>
              {logins?.map((l) => (
                <option key={l.name} value={l.name} disabled={l.master}>
                  {l.master
                    ? t("grant.masterLogin", { login: l.name })
                    : l.disabled
                      ? t("grant.disabledLogin", { login: l.name })
                      : l.name}
                </option>
              ))}
            </select>
          </label>
        )}

        {creating && (
          <div className="grant-new">
            <label className="field-inline">
              <span>{t("grant.name")}</span>
              <input
                className="input mono"
                name="newLogin"
                value={login}
                autoComplete="off"
                spellCheck={false}
                disabled={disabled}
                aria-invalid={nameInvalid || nameTaken || undefined}
                aria-describedby={nameInvalid || nameTaken ? `${ids}-name-error` : undefined}
                onChange={(e) => onChange({ login: e.currentTarget.value })}
              />
            </label>
            {(nameInvalid || nameTaken) && (
              <p id={`${ids}-name-error`} className="field-error">
                {t(nameTaken ? "grant.nameTaken" : "grant.nameInvalid")}
              </p>
            )}
            <div className="field-inline">
              <label htmlFor={`${ids}-pw`}>{t("grant.password")}</label>
              <div className="password-row">
                <input
                  id={`${ids}-pw`}
                  className="input mono"
                  name="loginPassword"
                  type={shown ? "text" : "password"}
                  value={password}
                  autoComplete="new-password"
                  spellCheck={false}
                  disabled={disabled}
                  aria-invalid={problem !== null || undefined}
                  aria-describedby={`${ids}-pw-note${problem ? ` ${ids}-pw-error` : ""}`}
                  onChange={(e) => {
                    onPassword(e.currentTarget.value);
                    setCopied(false);
                  }}
                />
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  aria-pressed={shown}
                  disabled={disabled}
                  onClick={() => setShown((s) => !s)}
                >
                  {shown ? (
                    <EyeOff size={14} aria-hidden="true" />
                  ) : (
                    <Eye size={14} aria-hidden="true" />
                  )}
                  {t("grant.show")}
                </button>
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  disabled={disabled}
                  onClick={() => {
                    onPassword(generatePassword());
                    setShown(true);
                    setCopied(false);
                  }}
                >
                  <KeyRound size={14} aria-hidden="true" />
                  {t("grant.generate")}
                </button>
                <button
                  type="button"
                  className="btn btn-outline btn-sm"
                  disabled={disabled || password === ""}
                  onClick={() => void copy()}
                >
                  {copied ? (
                    <Check size={14} aria-hidden="true" />
                  ) : (
                    <Copy size={14} aria-hidden="true" />
                  )}
                  {t("grant.copy")}
                </button>
              </div>
            </div>
            <p id={`${ids}-pw-note`} className="hint">
              {t("grant.passwordNote")}
            </p>
            {problem && (
              <p id={`${ids}-pw-error`} className="field-error">
                {t(problem === "length" ? "grant.pw.length" : "grant.pw.classes")}
              </p>
            )}
            <span className="sr-only" role="status">
              {copied ? t("grant.copied") : ""}
            </span>
          </div>
        )}
      </fieldset>

      <fieldset className="form-section grant-fieldset">
        <legend>{t("grant.level")}</legend>
        {GRANT_LEVELS.map((l) => (
          <div key={l.level} className="check">
            <input
              id={`${ids}-level-${l.level}`}
              type="radio"
              name={`${ids}-level`}
              checked={params.level === l.level}
              disabled={disabled}
              aria-describedby={`${ids}-level-${l.level}-hint`}
              onChange={() => onChange({ level: l.level })}
            />
            <div>
              <label htmlFor={`${ids}-level-${l.level}`}>{t(l.label)}</label>
              <p id={`${ids}-level-${l.level}-hint`} className="hint">
                {t(l.hint)}
              </p>
            </div>
          </div>
        ))}
      </fieldset>

      <div className="check">
        <input
          id={`${ids}-backup`}
          type="checkbox"
          name="backup"
          checked={!!params.backup}
          disabled={disabled}
          aria-describedby={`${ids}-backup-hint`}
          onChange={(e) => onChange({ backup: e.currentTarget.checked })}
        />
        <div>
          <label htmlFor={`${ids}-backup`}>{t("grant.backup")}</label>
          <p id={`${ids}-backup-hint`} className="hint">
            {t("grant.backupHint")}
          </p>
        </div>
      </div>
    </>
  );
}

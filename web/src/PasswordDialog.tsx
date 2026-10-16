import { Eye, EyeOff, LoaderCircle } from "lucide-react";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import { errorText, listTargets, setPassword, type TargetSummary } from "./api";
import type { EngineInfo } from "./engine";
import { HintText } from "./Feedback";
import { passwordHint } from "./hints";
import { useI18n } from "./i18n";
import { Modal } from "./Modal";

/**
 * Asks for the SQL password of a target and gives it to the engine, which
 * logs in with it before keeping it. The value lives only in this dialog's
 * state and in the request body: it is cleared when the dialog closes.
 * onSaved runs once the engine has accepted it.
 */
export function PasswordDialog({
  engine,
  target,
  onClose,
  onSaved,
}: {
  engine: EngineInfo;
  target: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const ids = useId();
  const input = useRef<HTMLInputElement>(null);
  const [summary, setSummary] = useState<TargetSummary | null>(null);
  const [password, setValue] = useState("");
  const [show, setShow] = useState(false);
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [empty, setEmpty] = useState(false);
  // Counts the refusals: the focus goes back to the field after each one.
  const [refused, setRefused] = useState(0);
  const abort = useRef<AbortController | null>(null);

  // The SQL user and whether a keyring exists: from the same list as the cards.
  useEffect(() => {
    let live = true;
    listTargets(engine).then(
      (list) => live && setSummary(list.targets.find((x) => x.name === target) ?? null),
      () => undefined,
    );
    return () => {
      live = false;
      abort.current?.abort();
    };
  }, [engine, target]);

  // The dialog exists for this one field.
  useEffect(() => input.current?.focus(), []);

  // After an async refusal: the message is there, the focus returns to the field.
  useEffect(() => {
    if (refused > 0) {
      input.current?.focus();
      input.current?.select();
    }
  }, [refused]);

  const canRemember = summary?.password?.canRemember ?? false;
  const close = () => {
    setValue("");
    onClose();
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) {
      return;
    }
    if (password === "") {
      setEmpty(true);
      setError(null);
      input.current?.focus();
      return;
    }
    setEmpty(false);
    setBusy(true);
    setError(null);
    const controller = new AbortController();
    abort.current = controller;
    try {
      await setPassword(engine, target, password, remember && canRemember, controller.signal);
      setValue("");
      onSaved();
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err);
        setRefused((n) => n + 1);
      }
    } finally {
      setBusy(false);
    }
  };

  const fieldId = `${ids}-password`;
  const describedBy = [`${ids}-lead`, empty ? `${ids}-empty` : ""].filter(Boolean).join(" ");
  return (
    <Modal
      title={t("password.title", { target })}
      onClose={close}
      busy={busy}
      footer={
        <>
          <button type="button" className="btn btn-outline" onClick={close}>
            {t("password.cancel")}
          </button>
          {/* Busy, not disabled: a disabled button would drop the focus out of the form. */}
          <button
            type="submit"
            form={`${ids}-form`}
            className="btn btn-primary"
            aria-disabled={busy || undefined}
          >
            {busy && <LoaderCircle size={16} className="spin" aria-hidden="true" />}
            {busy ? t("password.connecting") : t("password.connect")}
          </button>
        </>
      }
    >
      <form id={`${ids}-form`} onSubmit={submit} noValidate>
        {summary?.sqlUser && (
          <p className="muted">{t("password.user", { user: summary.sqlUser })}</p>
        )}
        <p id={`${ids}-lead`} className="hint">
          {t("password.lead")}
        </p>
        <div className="form-field">
          <label htmlFor={fieldId}>{t("password.label")}</label>
          <div className="input-group">
            <input
              ref={input}
              id={fieldId}
              name="password"
              className="input"
              type={show ? "text" : "password"}
              value={password}
              onChange={(e) => {
                setValue(e.target.value);
                setEmpty(false);
              }}
              autoComplete="current-password"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              aria-required="true"
              aria-invalid={empty || error !== null ? true : undefined}
              aria-describedby={describedBy}
            />
            <button
              type="button"
              className="btn btn-outline btn-icon"
              onClick={() => setShow((v) => !v)}
              aria-pressed={show}
              aria-label={t("password.show")}
              title={t("password.show")}
            >
              {show ? (
                <EyeOff size={16} aria-hidden="true" />
              ) : (
                <Eye size={16} aria-hidden="true" />
              )}
            </button>
          </div>
          {empty && (
            <p id={`${ids}-empty`} className="field-error">
              {t("password.empty")}
            </p>
          )}
        </div>
        <div className="check">
          <input
            id={`${ids}-remember`}
            type="checkbox"
            checked={remember && canRemember}
            disabled={!canRemember}
            onChange={(e) => setRemember(e.target.checked)}
            aria-describedby={canRemember ? undefined : `${ids}-remember-hint`}
          />
          <div>
            <label htmlFor={`${ids}-remember`}>{t("password.remember")}</label>
            {!canRemember && (
              <p id={`${ids}-remember-hint`} className="hint">
                {t("password.rememberUnavailable")}
              </p>
            )}
          </div>
        </div>
        {error !== null && (
          <div className="banner banner-fail" role="alert">
            <span className="banner-text">{errorText(error)}</span>
            <HintText hint={passwordHint(error)} />
          </div>
        )}
      </form>
    </Modal>
  );
}

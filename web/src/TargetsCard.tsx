import { Trash2 } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { errorText, listTargets, removeTarget, type TargetSummary } from "./api";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";

/** The configured targets, each with Remove (retype the name to confirm). */
export function TargetsCard({ engine }: { engine: EngineInfo | null }) {
  const { t } = useI18n();
  const [targets, setTargets] = useState<TargetSummary[] | null>(null);
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState("");
  const [typed, setTyped] = useState("");
  const [removing, setRemoving] = useState(false);
  const [removeError, setRemoveError] = useState("");
  const [removed, setRemoved] = useState("");
  const headingRef = useRef<HTMLHeadingElement>(null);

  const load = useCallback(async () => {
    if (!engine) {
      return;
    }
    try {
      const list = await listTargets(engine);
      setTargets(list.targets);
      setError(list.error ?? "");
    } catch (e) {
      setError(errorText(e));
    }
  }, [engine]);

  useEffect(() => {
    load();
  }, [load]);

  function ask(name: string) {
    setConfirming(name);
    setTyped("");
    setRemoveError("");
    setRemoved("");
  }

  function cancel() {
    const name = confirming;
    // The Remove button is disabled while it asks: enable it before it takes the focus back.
    flushSync(() => setConfirming(""));
    document.getElementById(`remove-${name}`)?.focus();
  }

  async function confirm(event: FormEvent) {
    event.preventDefault();
    if (!engine || typed !== confirming) {
      return;
    }
    setRemoving(true);
    setRemoveError("");
    try {
      await removeTarget(engine, confirming);
      setRemoved(confirming);
      setConfirming("");
      await load();
      headingRef.current?.focus();
    } catch (e) {
      setRemoveError(errorText(e));
    } finally {
      setRemoving(false);
    }
  }

  return (
    <section className="card settings" aria-labelledby="settings-targets">
      <div>
        <h2 id="settings-targets" ref={headingRef} tabIndex={-1}>
          {t("settings.targets")}
        </h2>
        <p className="hint">{t("settings.targetsLead")}</p>
      </div>
      {removed && (
        <div className="banner banner-ok" role="status">
          {t("targets.removed", { name: removed })}
        </div>
      )}
      {error && (
        <div className="banner banner-fail" role="alert">
          <pre className="error-pre">{error}</pre>
        </div>
      )}
      {targets === null && !error && <div className="skeleton skeleton-row" aria-hidden="true" />}
      {targets?.length === 0 && !error && <p className="muted">{t("settings.noTargets")}</p>}
      {targets && targets.length > 0 && (
        <ul className="item-list">
          {targets.map((target) => (
            <li key={target.name} className="item">
              <div className="item-row">
                <span className="item-title mono">{target.name}</span>
                {target.protected && (
                  <span className="badge badge-warning">{t("target.protected")}</span>
                )}
                <button
                  id={`remove-${target.name}`}
                  type="button"
                  className="btn btn-outline btn-sm"
                  onClick={() => ask(target.name)}
                  disabled={confirming === target.name}
                  aria-label={`${t("targets.remove")} ${target.name}`}
                >
                  <Trash2 size={14} aria-hidden="true" />
                  {t("targets.remove")}
                </button>
              </div>
              <div className="muted">
                {target.instance} · {target.region} · {target.profile}
              </div>
              {confirming === target.name && (
                <form
                  className="confirm-remove"
                  onSubmit={confirm}
                  aria-labelledby={`remove-${target.name}-title`}
                  onKeyDown={(e) => e.key === "Escape" && cancel()}
                >
                  <strong id={`remove-${target.name}-title`}>
                    {t("targets.removeTitle", { name: target.name })}
                  </strong>
                  <p id={`remove-${target.name}-hint`} className="hint">
                    {t("targets.removeHint", { name: target.name })}
                  </p>
                  <div className="form-field">
                    <label htmlFor={`remove-${target.name}-confirm`}>
                      {t("targets.confirmLabel")}
                    </label>
                    <input
                      id={`remove-${target.name}-confirm`}
                      className="input mono"
                      value={typed}
                      onChange={(e) => setTyped(e.target.value)}
                      autoComplete="off"
                      spellCheck={false}
                      // biome-ignore lint/a11y/noAutofocus: the field appears on request and must take the focus
                      autoFocus
                      aria-describedby={`remove-${target.name}-hint`}
                    />
                  </div>
                  {removeError && (
                    <div className="banner banner-fail" role="alert">
                      <span className="banner-text">{removeError}</span>
                    </div>
                  )}
                  <div className="actions">
                    <button type="button" className="btn btn-outline" onClick={cancel}>
                      {t("add.cancel")}
                    </button>
                    <button
                      type="submit"
                      className="btn btn-destructive"
                      disabled={typed !== target.name || removing}
                    >
                      <Trash2 size={16} aria-hidden="true" />
                      {t("targets.confirmRemove")}
                    </button>
                  </div>
                </form>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

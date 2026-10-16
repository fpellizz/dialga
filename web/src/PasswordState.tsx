import { useState } from "react";
import { errorText, forgetPassword, type PasswordInfo, type TargetSummary } from "./api";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";
import { usePassword } from "./PasswordProvider";

/** Where the SQL password of a target comes from and whether the engine has it. */
export function PasswordState({ engine, target }: { engine: EngineInfo; target: TargetSummary }) {
  const { t } = useI18n();
  const { ask, changed } = usePassword();
  const [error, setError] = useState("");
  const info: PasswordInfo | undefined = target.password;
  if (!info) {
    return null;
  }

  let text = t("password.state.secretsmanager");
  if (info.source === "prompt") {
    text = t(info.available ? "password.state.promptSet" : "password.state.promptMissing");
  } else if (info.source === "env") {
    text = info.envVar
      ? t("password.state.envVar", { name: info.envVar })
      : t("password.state.env");
  } else if (info.source === "keyring") {
    text = t("password.state.keyring");
  }
  // Only what the engine holds can be forgotten: memory and keyring.
  const canForget = (info.source === "prompt" || info.source === "keyring") && info.available;
  const canEnter = info.source === "prompt" && !info.available;

  const forget = async () => {
    setError("");
    try {
      await forgetPassword(engine, target.name);
      changed();
    } catch (e) {
      setError(errorText(e));
    }
  };

  return (
    <>
      <dt>{t("password.label.status")}</dt>
      <dd className="password-state">
        <span>{text}</span>
        {canEnter && (
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => ask(target.name)}
            aria-haspopup="dialog"
            aria-label={t("password.enterFor", { target: target.name })}
          >
            {t("password.enter")}
          </button>
        )}
        {canForget && (
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={forget}
            aria-label={t("password.forgetFor", { target: target.name })}
          >
            {t("password.forget")}
          </button>
        )}
        {error && <span className="field-error">{error}</span>}
      </dd>
    </>
  );
}

import { errorText, passwordNeeded } from "./api";
import { errorHint, type Hint } from "./hints";
import { useI18n } from "./i18n";
import { recordedRefusal } from "./jobs";
import { PasswordNeededBanner } from "./PasswordProvider";

/** What to do, under a message; nothing without a hint. */
export function HintText({ hint, className = "banner-hint" }: { hint?: Hint; className?: string }) {
  const { t } = useI18n();
  return hint ? <span className={className}>{t(hint.key, hint.vars)}</span> : null;
}

/**
 * A failed call: the engine's message (the cause), then what to do.
 * Preformatted keeps the lines of a multi-line message (configuration errors).
 */
export function ErrorBanner({
  error,
  fallback,
  preformatted = false,
  recorded = false,
}: {
  error: unknown;
  fallback?: Hint;
  preformatted?: boolean;
  /** The error is a refused Run: the engine records it in Activity (link to it). */
  recorded?: boolean;
}) {
  const { t } = useI18n();
  const needed = passwordNeeded(error);
  if (needed) {
    return <PasswordNeededBanner target={needed} />;
  }
  return (
    <div className="banner banner-fail" role="alert">
      {preformatted ? (
        <pre className="error-pre">{errorText(error)}</pre>
      ) : (
        <span className="banner-text">{errorText(error)}</span>
      )}
      <HintText hint={errorHint(error) ?? fallback} />
      {recorded && recordedRefusal(error) && (
        <a className="banner-hint" href="#activity">
          {t("activity.seeIt")}
        </a>
      )}
    </div>
  );
}

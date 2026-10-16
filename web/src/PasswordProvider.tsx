import { KeyRound } from "lucide-react";
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
} from "react";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";
import { PasswordDialog } from "./PasswordDialog";

interface PasswordContextValue {
  /** Grows after every saved or forgotten password: loads depend on it to run again. */
  epoch: number;
  /** Opens the dialog for a target. */
  ask: (target: string) => void;
  /** The passwords changed outside the dialog (forgotten). */
  changed: () => void;
}

const PasswordContext = createContext<PasswordContextValue>({
  epoch: 0,
  ask: () => undefined,
  changed: () => undefined,
});

/** The dialog of the SQL password, and the counter that tells loads to retry. */
export function PasswordProvider({
  engine,
  children,
}: {
  engine: EngineInfo | null;
  children: ReactNode;
}) {
  const [asking, setAsking] = useState<string | null>(null);
  const [epoch, setEpoch] = useState(0);
  const ask = useCallback((target: string) => setAsking(target), []);
  const changed = useCallback(() => setEpoch((n) => n + 1), []);
  const value = useMemo(() => ({ epoch, ask, changed }), [epoch, ask, changed]);
  return (
    <PasswordContext.Provider value={value}>
      {children}
      {engine && asking !== null && (
        <PasswordDialog
          engine={engine}
          target={asking}
          onClose={() => setAsking(null)}
          onSaved={() => {
            setAsking(null);
            changed();
          }}
        />
      )}
    </PasswordContext.Provider>
  );
}

export function usePassword(): PasswordContextValue {
  return useContext(PasswordContext);
}

/**
 * Instead of the raw 428: says the password is needed and opens the dialog.
 * Loads run again by themselves once it is saved (they depend on epoch); an
 * action that failed shows that it has to be repeated.
 */
export function PasswordNeededBanner({ target }: { target: string }) {
  const { t } = useI18n();
  const { epoch, ask } = usePassword();
  const mounted = useRef(epoch);
  if (epoch !== mounted.current) {
    return (
      <div className="banner banner-ok" role="status">
        <span className="banner-text">{t("password.savedRetry")}</span>
      </div>
    );
  }
  return (
    <div className="banner banner-warn" role="alert">
      <KeyRound size={16} aria-hidden="true" />
      <span className="banner-text">{t("password.needed", { target })}</span>
      <button
        type="button"
        className="btn btn-outline btn-sm"
        onClick={() => ask(target)}
        aria-haspopup="dialog"
        aria-label={t("password.enterFor", { target })}
      >
        {t("password.enter")}
      </button>
    </div>
  );
}

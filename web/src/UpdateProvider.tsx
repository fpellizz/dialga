import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { getUpdateInfo, type UpdateInfo } from "./api";
import type { EngineInfo } from "./engine";
import {
  loadCheckPref,
  loadDismissed,
  type Release,
  releaseOf,
  saveCheckPref,
  saveDismissed,
} from "./updates";

/** The app asks the engine again this often while it stays open. */
export const UPDATE_INTERVAL_MS = 12 * 60 * 60 * 1000;

interface Updates {
  /** The "Check for updates" setting. */
  enabled: boolean;
  setEnabled: (on: boolean) => void;
  /** The last answer of the engine (null: none yet, or the call failed). */
  info: UpdateInfo | null;
  checking: boolean;
  /** Asks GitHub now (ignoring the cache); null when the engine call failed. */
  checkNow: () => Promise<UpdateInfo | null>;
  /** A newer release exists (also one whose notice was dismissed): the About page. */
  available: Release | null;
  /** The release the sidebar notice shows: available and not dismissed. */
  notice: Release | null;
  dismiss: () => void;
}

/** Without a provider (tests of single components): nothing to report. */
const IDLE: Updates = {
  enabled: true,
  setEnabled: () => {},
  info: null,
  checking: false,
  checkNow: async () => null,
  available: null,
  notice: null,
  dismiss: () => {},
};

const UpdatesContext = createContext<Updates>(IDLE);

export function useUpdates(): Updates {
  return useContext(UpdatesContext);
}

/**
 * Asks the engine whether a newer release exists, at start and every 12
 * hours, when the setting is on. A failed call is silent here: the notice
 * simply does not show; only "Check now" reports it.
 */
export function UpdateProvider({
  engine,
  children,
}: {
  engine: EngineInfo | null;
  children: ReactNode;
}) {
  const [enabled, setEnabledState] = useState(loadCheckPref);
  const [dismissed, setDismissed] = useState(loadDismissed);
  const [info, setInfo] = useState<UpdateInfo | null>(null);
  const [checking, setChecking] = useState(false);
  const live = useRef(true);

  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const run = useCallback(
    async (force: boolean): Promise<UpdateInfo | null> => {
      if (!engine) {
        return null;
      }
      if (force) {
        setChecking(true);
      }
      try {
        const next = await getUpdateInfo(engine, force);
        if (live.current) {
          setInfo(next);
        }
        return next;
      } catch {
        return null;
      } finally {
        if (force && live.current) {
          setChecking(false);
        }
      }
    },
    [engine],
  );

  useEffect(() => {
    if (!engine || !enabled) {
      return;
    }
    void run(false);
    const timer = setInterval(() => void run(false), UPDATE_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [engine, enabled, run]);

  const available = enabled ? releaseOf(info) : null;
  const value = useMemo<Updates>(
    () => ({
      enabled,
      setEnabled: (on) => {
        saveCheckPref(on);
        setEnabledState(on);
      },
      info,
      checking,
      checkNow: () => run(true),
      available,
      notice: available && available.version !== dismissed ? available : null,
      dismiss: () => {
        if (available) {
          saveDismissed(available.version);
          setDismissed(available.version);
        }
      },
    }),
    [enabled, info, checking, run, available, dismissed],
  );
  return <UpdatesContext.Provider value={value}>{children}</UpdatesContext.Provider>;
}

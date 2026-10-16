import { Cable, LoaderCircle, Unplug } from "lucide-react";
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
import {
  closeTunnel,
  errorText,
  listTunnels,
  openTunnel,
  type TunnelState,
  watchTunnel,
} from "./api";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";
import {
  retryDelay,
  tunnelAnnouncement,
  tunnelDot,
  tunnelIsLive,
  tunnelStatusKey,
  tunnelSummary,
} from "./tunnel";

/** Bumped when a card sees a tunnel change state, so the sidebar summary reloads. */
const TunnelsContext = createContext<{ version: number; changed: () => void }>({
  version: 0,
  changed: () => {},
});

export function TunnelsProvider({ children }: { children: ReactNode }) {
  const [version, setVersion] = useState(0);
  const changed = useCallback(() => setVersion((v) => v + 1), []);
  const value = useMemo(() => ({ version, changed }), [version, changed]);
  return <TunnelsContext.Provider value={value}>{children}</TunnelsContext.Provider>;
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    });
  });

/** The live state of a target's shared tunnel, following the engine events. */
function useTunnel(engine: EngineInfo, target: string, onChange: (state: TunnelState) => void) {
  const [state, setState] = useState<TunnelState | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    (async () => {
      for (let attempt = 0; !signal.aborted; attempt++) {
        try {
          await watchTunnel(
            engine,
            target,
            (next) => {
              attempt = 0;
              setState(next);
              onChangeRef.current(next);
            },
            signal,
          );
        } catch {
          // The engine restarted or the stream dropped: follow it again.
        }
        await sleep(retryDelay(attempt + 1), signal);
      }
    })();
    return () => controller.abort();
  }, [engine, target]);
  return state;
}

/** State, port and bridge of a tunnel-mode target's shared tunnel, with Connect/Disconnect. */
export function TunnelIndicator({ engine, target }: { engine: EngineInfo; target: string }) {
  const { t } = useI18n();
  const { changed } = useContext(TunnelsContext);
  const [announce, setAnnounce] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const previous = useRef<TunnelState | null>(null);

  const state = useTunnel(engine, target, (next) => {
    const key = tunnelAnnouncement(previous.current, next);
    if (key) {
      setAnnounce(t(key, { target, port: next.localPort ?? 0 }));
    }
    if (previous.current?.status !== next.status) {
      changed();
    }
    previous.current = next;
  });

  async function act(action: typeof openTunnel) {
    setBusy(true);
    setError("");
    try {
      await action(engine, target);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const live = state ? tunnelIsLive(state.status) : false;
  const title = state
    ? [state.detail, state.users > 0 ? t("tunnel.users", { count: state.users }) : ""]
    : [];
  if (state && live) {
    title.push(state.keepOpen ? t("tunnel.keepOpen") : t("tunnel.byUse"));
  }
  return (
    <div className="tunnel">
      {!state ? (
        <div className="skeleton skeleton-line tunnel-skeleton" aria-hidden="true" />
      ) : (
        <div className="tunnel-row">
          <span className={tunnelDot(state)} aria-hidden="true" />
          <span className="tunnel-text" title={title.filter(Boolean).join("\n") || undefined}>
            <span className="tunnel-status">{t(tunnelStatusKey(state))}</span>
            {live && state.localPort ? (
              <span className="mono muted">127.0.0.1:{state.localPort}</span>
            ) : null}
            {live && state.bridge ? (
              <span className="mono muted">{t("tunnel.via", { bridge: state.bridge })}</span>
            ) : null}
          </span>
          <button
            type="button"
            className="btn btn-outline btn-sm"
            onClick={() => act(live ? closeTunnel : openTunnel)}
            disabled={busy}
          >
            {busy ? (
              <LoaderCircle size={14} className="spin" aria-hidden="true" />
            ) : live ? (
              <Unplug size={14} aria-hidden="true" />
            ) : (
              <Cable size={14} aria-hidden="true" />
            )}
            {live ? t("tunnel.disconnect") : t("tunnel.connect")}
          </button>
        </div>
      )}
      {(error || (state?.failed && state.detail)) && (
        <p className="tunnel-detail tone-destructive">{error || state?.detail}</p>
      )}
      {!error && state?.status === "reconnecting" && state.detail && (
        <p className="tunnel-detail tone-warning">{state.detail}</p>
      )}
      <span className="sr-only" role="status" aria-live="polite">
        {announce}
      </span>
    </div>
  );
}

/** Tunnels open, for the sidebar footer: nothing when none is up. */
export function TunnelSummary({ engine }: { engine: EngineInfo | null }) {
  const { t } = useI18n();
  const { version } = useContext(TunnelsContext);
  const [list, setList] = useState<TunnelState[]>([]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: version is the reload trigger.
  useEffect(() => {
    if (!engine) {
      return;
    }
    const controller = new AbortController();
    const load = () =>
      listTunnels(engine, controller.signal).then(setList, () => {
        // No configuration or engine gone: no summary.
        if (!controller.signal.aborted) {
          setList([]);
        }
      });
    load();
    const timer = setInterval(load, 30_000);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, [engine, version]);

  const { ready, busy } = tunnelSummary(list);
  if (ready + busy === 0) {
    return null;
  }
  let text = ready === 1 ? t("tunnels.open.one") : t("tunnels.open.other", { count: ready });
  if (busy > 0) {
    text =
      ready > 0
        ? `${text} · ${t("tunnels.busy", { count: busy })}`
        : t("tunnels.busy", { count: busy });
  }
  return (
    <div className="engine">
      <span className={busy > 0 ? "dot dot-wait" : "dot"} aria-hidden="true" />
      {text}
    </div>
  );
}

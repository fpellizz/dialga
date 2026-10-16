import type { TunnelState, TunnelStatus } from "./api";
import type { MessageKey } from "./i18n";

/** The dot of a tunnel: green when ready, amber on the way, red after a failure. */
export function tunnelDot(state: Pick<TunnelState, "status" | "failed">): string {
  switch (state.status) {
    case "ready":
      return "dot";
    case "connecting":
    case "reconnecting":
      return "dot dot-wait";
    default:
      return state.failed ? "dot dot-off" : "dot dot-idle";
  }
}

/** The label of a tunnel state. */
export function tunnelStatusKey(state: Pick<TunnelState, "status" | "failed">): MessageKey {
  if (state.status === "closed" && state.failed) {
    return "tunnel.status.failed";
  }
  return `tunnel.status.${state.status}` as MessageKey;
}

/** The tunnel is up or on its way: Disconnect makes sense, Connect does not. */
export function tunnelIsLive(status: TunnelStatus): boolean {
  return status !== "closed";
}

/**
 * What a screen reader hears when the state changes: the end points only
 * (connected, reconnecting, closed, failed), never the first state nor the
 * changes of the number of users.
 */
export function tunnelAnnouncement(
  previous: TunnelState | null,
  next: TunnelState,
): MessageKey | null {
  if (!previous || previous.status === next.status) {
    return null;
  }
  switch (next.status) {
    case "ready":
      return "tunnel.announce.ready";
    case "reconnecting":
      return "tunnel.announce.reconnecting";
    case "closed":
      return next.failed ? "tunnel.announce.failed" : "tunnel.announce.closed";
    default:
      return null;
  }
}

/** Tunnels ready and on their way, for the sidebar. */
export function tunnelSummary(list: TunnelState[]): { ready: number; busy: number } {
  let ready = 0;
  let busy = 0;
  for (const tunnel of list) {
    if (tunnel.status === "ready") {
      ready++;
    } else if (tunnel.status !== "closed") {
      busy++;
    }
  }
  return { ready, busy };
}

/** Delay before following the events again after the stream dropped: 1 s doubling to 30 s. */
export function retryDelay(attempt: number): number {
  return Math.min(1000 * 2 ** Math.max(0, attempt - 1), 30_000);
}

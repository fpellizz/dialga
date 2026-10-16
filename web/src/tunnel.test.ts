import { describe, expect, it } from "vitest";
import type { TunnelState, TunnelStatus } from "./api";
import { messages, translate } from "./i18n";
import {
  retryDelay,
  tunnelAnnouncement,
  tunnelDot,
  tunnelIsLive,
  tunnelStatusKey,
  tunnelSummary,
} from "./tunnel";

function tunnel(status: TunnelStatus, more: Partial<TunnelState> = {}): TunnelState {
  return { target: "mssql-dev", status, users: 0, keepOpen: false, ...more };
}

const STATUSES: TunnelStatus[] = ["connecting", "ready", "reconnecting", "closed"];

describe("tunnelDot and tunnelStatusKey", () => {
  it("colours the dot by state", () => {
    expect(tunnelDot(tunnel("ready"))).toBe("dot");
    expect(tunnelDot(tunnel("connecting"))).toBe("dot dot-wait");
    expect(tunnelDot(tunnel("reconnecting"))).toBe("dot dot-wait");
    expect(tunnelDot(tunnel("closed"))).toBe("dot dot-idle");
    expect(tunnelDot(tunnel("closed", { failed: true }))).toBe("dot dot-off");
  });

  it("labels every state, in both languages", () => {
    for (const status of STATUSES) {
      expect(messages.en).toHaveProperty(tunnelStatusKey(tunnel(status)));
      expect(messages.it).toHaveProperty(tunnelStatusKey(tunnel(status)));
    }
    expect(tunnelStatusKey(tunnel("closed", { failed: true }))).toBe("tunnel.status.failed");
    expect(tunnelStatusKey(tunnel("ready"))).toBe("tunnel.status.ready");
  });

  it("offers Disconnect while the tunnel is up or on its way", () => {
    expect(STATUSES.filter(tunnelIsLive)).toEqual(["connecting", "ready", "reconnecting"]);
  });
});

describe("tunnelAnnouncement", () => {
  it("is silent on the first state and when only the users change", () => {
    expect(tunnelAnnouncement(null, tunnel("ready"))).toBeNull();
    expect(tunnelAnnouncement(tunnel("ready"), tunnel("ready", { users: 3 }))).toBeNull();
    expect(tunnelAnnouncement(tunnel("closed"), tunnel("connecting"))).toBeNull();
  });

  it("announces the end points of a change", () => {
    expect(tunnelAnnouncement(tunnel("connecting"), tunnel("ready"))).toBe("tunnel.announce.ready");
    expect(tunnelAnnouncement(tunnel("ready"), tunnel("reconnecting"))).toBe(
      "tunnel.announce.reconnecting",
    );
    expect(tunnelAnnouncement(tunnel("ready"), tunnel("closed"))).toBe("tunnel.announce.closed");
    expect(tunnelAnnouncement(tunnel("connecting"), tunnel("closed", { failed: true }))).toBe(
      "tunnel.announce.failed",
    );
  });

  it("names the target and the port", () => {
    expect(translate("en")("tunnel.announce.ready", { target: "mssql-dev", port: 41433 })).toBe(
      "Tunnel to mssql-dev connected on port 41433",
    );
    expect(translate("it")("tunnel.announce.ready", { target: "mssql-dev", port: 41433 })).toBe(
      "Tunnel verso mssql-dev connesso sulla porta 41433",
    );
  });
});

describe("tunnelSummary", () => {
  it("counts the tunnels ready and on their way", () => {
    expect(tunnelSummary([])).toEqual({ ready: 0, busy: 0 });
    expect(
      tunnelSummary([
        tunnel("ready"),
        tunnel("ready"),
        tunnel("reconnecting"),
        tunnel("connecting"),
        tunnel("closed", { failed: true }),
      ]),
    ).toEqual({ ready: 2, busy: 2 });
  });
});

describe("retryDelay", () => {
  it("doubles from 1 s up to 30 s", () => {
    expect([1, 2, 3, 5, 6, 20].map(retryDelay)).toEqual([1000, 2000, 4000, 16000, 30000, 30000]);
    expect(retryDelay(0)).toBe(1000);
  });
});

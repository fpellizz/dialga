import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { TunnelState } from "./api";
import { TunnelIndicator, TunnelSummary, TunnelsProvider } from "./TunnelIndicator";
import { renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";

const EVENTS = "GET /api/v1/targets/lab-sql/tunnel/events";
const OPEN = "POST /api/v1/targets/lab-sql/tunnel";
const CLOSE = "DELETE /api/v1/targets/lab-sql/tunnel";
const TUNNELS = "GET /api/v1/tunnels";

const CLOSED: TunnelState = { target: "lab-sql", status: "closed", users: 0, keepOpen: false };
const READY: TunnelState = {
  target: "lab-sql",
  status: "ready",
  localPort: 41433,
  endpoint: "my-sqlserver-lab.abc123.eu-west-1.rds.amazonaws.com:1433",
  bridge: "i-0abc123",
  detail: "via i-0abc123 to my-sqlserver-lab:1433",
  users: 1,
  keepOpen: true,
};

/** The engine's event stream for the tunnel: the test sends the states. */
function events() {
  const stream = sseStream();
  return {
    route: () => stream.response,
    state: (state: TunnelState) => act(() => stream.send("state", state)),
    end: () => act(() => stream.close()),
  };
}

function indicator() {
  return renderI18n(<TunnelIndicator engine={ENGINE} target="lab-sql" />);
}

function announced() {
  return screen.getByRole("status");
}

describe("TunnelIndicator", () => {
  it("waits for the first state, then offers to connect", async () => {
    const tunnel = events();
    mockFetch({ [EVENTS]: tunnel.route });
    const { container } = indicator();
    expect(container.querySelector(".skeleton")).not.toBeNull();

    await tunnel.state(CLOSED);
    expect(await screen.findByText("Not connected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
    // The first state is not news: nothing is announced.
    expect(announced()).toBeEmptyDOMElement();
  });

  it("connects, follows the states and announces the end points", async () => {
    const tunnel = events();
    const opened = deferred<void>();
    const fetch = mockFetch({
      [EVENTS]: tunnel.route,
      [OPEN]: async () => {
        await opened.promise;
        return json(READY);
      },
    });
    const { container } = indicator();
    await tunnel.state(CLOSED);

    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    expect(requestsTo(fetch, OPEN)).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();

    await tunnel.state({ ...CLOSED, status: "connecting" });
    expect(await screen.findByText("Connecting…")).toBeInTheDocument();
    expect(container.querySelector(".dot")).toHaveClass("dot-wait");

    await tunnel.state(READY);
    opened.resolve();
    expect(await screen.findByText("Connected")).toBeInTheDocument();
    expect(screen.getByText("127.0.0.1:41433")).toBeInTheDocument();
    expect(screen.getByText("via i-0abc123")).toBeInTheDocument();
    expect(screen.getByText("Connected").parentElement).toHaveAttribute(
      "title",
      "via i-0abc123 to my-sqlserver-lab:1433\n1 in use\nOpened on request: stays open until you disconnect it.",
    );
    expect(announced()).toHaveTextContent("Tunnel to lab-sql connected on port 41433");
    await waitFor(() => expect(screen.getByRole("button", { name: "Disconnect" })).toBeEnabled());
  });

  it("disconnects and announces it", async () => {
    const tunnel = events();
    const fetch = mockFetch({ [EVENTS]: tunnel.route, [CLOSE]: () => json(CLOSED) });
    indicator();
    await tunnel.state(READY);

    await userEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
    expect(requestsTo(fetch, CLOSE)).toHaveLength(1);
    await tunnel.state(CLOSED);
    expect(await screen.findByText("Not connected")).toBeInTheDocument();
    expect(announced()).toHaveTextContent("Tunnel to lab-sql closed");
  });

  it("shows why the tunnel failed or is reconnecting", async () => {
    const tunnel = events();
    mockFetch({ [EVENTS]: tunnel.route });
    indicator();
    await tunnel.state(READY);

    await tunnel.state({ ...READY, status: "reconnecting", detail: "bridge i-0abc123 stopped" });
    expect(await screen.findByText("Reconnecting…")).toBeInTheDocument();
    expect(screen.getByText("bridge i-0abc123 stopped")).toHaveClass("tone-warning");
    expect(announced()).toHaveTextContent("Tunnel to lab-sql reconnecting");

    await tunnel.state({ ...CLOSED, failed: true, detail: "no ssm-bridge instance in vpc-0abc" });
    expect(await screen.findByText("Connection failed")).toBeInTheDocument();
    expect(screen.getByText("no ssm-bridge instance in vpc-0abc")).toHaveClass("tone-destructive");
    expect(announced()).toHaveTextContent("Tunnel to lab-sql failed");
    expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
  });

  it("shows a refused Connect without the Error: prefix", async () => {
    const tunnel = events();
    mockFetch({
      [EVENTS]: tunnel.route,
      [OPEN]: () => text("session-manager-plugin not found: install it\n", 422),
    });
    indicator();
    await tunnel.state(CLOSED);
    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    expect(await screen.findByText("session-manager-plugin not found: install it")).toHaveClass(
      "tone-destructive",
    );
    expect(screen.getByRole("button", { name: "Connect" })).toBeEnabled();
  });

  it("follows the events again when the stream drops", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const first = events();
    const second = events();
    const fetch = mockFetch({
      [EVENTS]: (_req, call) => (call === 0 ? first.route() : second.route()),
    });
    indicator();
    await first.state(CLOSED);
    await first.end();

    // 1 s after the first drop (then doubling, up to 30 s).
    await act(() => vi.advanceTimersByTimeAsync(1_000));
    await waitFor(() => expect(requestsTo(fetch, EVENTS)).toHaveLength(2));
    await second.state(READY);
    expect(await screen.findByText("Connected")).toBeInTheDocument();
  });

  it("makes the sidebar summary reload when the status changes", async () => {
    const tunnel = events();
    const fetch = mockFetch({
      [EVENTS]: tunnel.route,
      [TUNNELS]: (_req, call) => json({ tunnels: call === 0 ? [] : [READY] }),
    });
    renderI18n(
      <TunnelsProvider>
        <TunnelSummary engine={ENGINE} />
        <TunnelIndicator engine={ENGINE} target="lab-sql" />
      </TunnelsProvider>,
    );
    await waitFor(() => expect(requestsTo(fetch, TUNNELS)).toHaveLength(1));
    await tunnel.state(CLOSED);
    await waitFor(() => expect(requestsTo(fetch, TUNNELS)).toHaveLength(2));

    await tunnel.state(READY);
    expect(await screen.findByText("1 tunnel open")).toBeInTheDocument();
    expect(requestsTo(fetch, TUNNELS)).toHaveLength(3);
  });
});

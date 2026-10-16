import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Prerequisite } from "./api";
import { PrerequisitesCard } from "./PrerequisitesCard";
import { renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch } from "./test/http";

const HINTS = [
  { distro: "fedora", command: "sudo dnf install -y podman" },
  { distro: "debian", command: "sudo apt-get install -y podman" },
] as const;

function item(name: string) {
  return within(screen.getByText(name).closest("li") as HTMLElement);
}

describe("PrerequisitesCard", () => {
  it("shows found, outdated, missing and unusable programs", async () => {
    const prerequisites: Prerequisite[] = [
      {
        id: "session-manager-plugin",
        program: "session-manager-plugin",
        found: true,
        path: "/usr/bin/session-manager-plugin",
        version: "1.2.400.0",
        outdated: true,
        purpose: "SSM tunnels",
        install: [...HINTS],
      },
      {
        id: "container-runtime",
        program: "docker",
        found: false,
        path: "/usr/bin/docker",
        error: "docker info: cannot connect to the Docker daemon",
        purpose: "inspect",
        install: [...HINTS],
      },
    ];
    mockFetch({ "GET /api/v1/prerequisites": () => json({ prerequisites }) });
    renderI18n(<PrerequisitesCard engine={ENGINE} />);

    const plugin = item(
      await screen.findByText("Session Manager plugin").then((e) => e.textContent ?? ""),
    );
    expect(plugin.getByText("Update needed")).toBeInTheDocument();
    expect(plugin.getByText(/Version 1\.2\.400\.0 is too old/)).toBeInTheDocument();
    expect(plugin.getByText("sudo dnf install -y podman")).toBeInTheDocument();

    const runtime = item("Container runtime");
    expect(runtime.getByText("Missing")).toBeInTheDocument();
    expect(
      runtime.getByText("docker info: cannot connect to the Docker daemon"),
    ).toBeInTheDocument();
    expect(runtime.getByText("Install on Debian/Ubuntu")).toBeInTheDocument();
  });

  it("lists a found program with its version and no install command", async () => {
    mockFetch({
      "GET /api/v1/prerequisites": () =>
        json({
          prerequisites: [
            {
              id: "container-runtime",
              program: "podman",
              found: true,
              path: "/usr/bin/podman",
              version: "5.8.4",
              purpose: "inspect",
              install: [...HINTS],
            },
          ],
        }),
    });
    renderI18n(<PrerequisitesCard engine={ENGINE} />, "it");
    const runtime = item(
      await screen.findByText("Runtime dei container").then((e) => e.textContent ?? ""),
    );
    expect(runtime.getByText("Trovato")).toBeInTheDocument();
    expect(runtime.getByText("podman 5.8.4 (/usr/bin/podman)")).toBeInTheDocument();
    expect(runtime.queryByText("sudo dnf install -y podman")).not.toBeInTheDocument();
  });
});

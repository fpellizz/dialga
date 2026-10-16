import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AddInstanceDialog } from "./AddInstanceDialog";
import type { DiscoveredInstance, InstanceDiscovery, TargetInput } from "./api";
import { DEV, discovered, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, type FetchMock, json, mockFetch, type Route, text } from "./test/http";

const PROFILES = "GET /api/v1/discovery/profiles";
const INSTANCES = "GET /api/v1/discovery/instances";
const SAVE = "POST /api/v1/config/targets";

const ACCOUNT = "123456789012";
const PROFILE_LIST = [
  { name: "sales", region: "us-east-1" },
  { name: "default", region: "eu-west-1" },
];

/** A discovery answer; a new Response for every call. */
function discovery(...instances: DiscoveredInstance[]): Response {
  const body: InstanceDiscovery = {
    profile: "default",
    region: "eu-west-1",
    accountId: ACCOUNT,
    instances,
  };
  return json(body);
}

function found(...instances: DiscoveredInstance[]): Route {
  return () => discovery(...instances);
}

function open(routes: Record<string, Route>, existing: string[] = []) {
  const fetch = mockFetch({ [PROFILES]: () => json({ profiles: PROFILE_LIST }), ...routes });
  const onClose = vi.fn();
  const onSaved = vi.fn();
  renderI18n(
    <AddInstanceDialog engine={ENGINE} existing={existing} onClose={onClose} onSaved={onSaved} />,
  );
  return { fetch, onClose, onSaved };
}

function dialog() {
  return screen.getByRole("dialog", { name: "Add instance" });
}

function field(name: string) {
  return within(dialog()).getByRole("textbox", { name });
}

/** The query strings of the discovery requests. */
function discoveries(fetch: FetchMock) {
  return fetch.mock.calls
    .map(([url]) => new URL(String(url)))
    .filter((url) => url.pathname === "/api/v1/discovery/instances")
    .map((url) => url.search);
}

function saved(fetch: FetchMock): TargetInput[] {
  return fetch.mock.calls
    .filter(([, init]) => init?.method === "POST")
    .map(([, init]) => JSON.parse(String(init?.body)) as TargetInput);
}

async function discover() {
  await userEvent.click(screen.getByRole("button", { name: "Discover instances" }));
}

describe("AddInstanceDialog", () => {
  it("opens as a modal dialog on the default profile and its region", async () => {
    open({});
    expect(dialog()).toHaveAttribute("open");
    expect(dialog()).toHaveAccessibleDescription(
      "Find a SQL Server instance with an AWS profile of this computer and save it as a target. Nothing changes on AWS.",
    );
    // Until the profiles arrive the field waits, disabled; Discover too.
    expect(within(dialog()).getByRole("textbox", { name: "AWS profile" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Discover instances" })).toBeDisabled();
    const profile = await within(dialog()).findByRole("combobox", { name: "AWS profile" });
    await waitFor(() => expect(profile).toHaveValue("default"));
    expect(profile).toHaveFocus();
    expect(screen.getByRole("button", { name: "Discover instances" })).toBeEnabled();
    expect(
      within(profile)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["sales (us-east-1)", "default (eu-west-1)"]);
    expect(field("Region")).toHaveValue("eu-west-1");
    expect(
      screen.getByText("Discover the instances of the profile, then choose one."),
    ).toBeInTheDocument();
    // Bucket, login and name come after an instance is chosen.
    expect(within(dialog()).queryByRole("textbox", { name: "S3 bucket" })).toBeNull();
  });

  it("lets the region follow the profile until it is edited", async () => {
    open({});
    const profile = await within(dialog()).findByRole("combobox", { name: "AWS profile" });
    await waitFor(() => expect(profile).toHaveValue("default"));
    await userEvent.selectOptions(profile, "sales");
    expect(field("Region")).toHaveValue("us-east-1");

    await userEvent.clear(field("Region"));
    await userEvent.type(field("Region"), "eu-south-1");
    await userEvent.selectOptions(profile, "default");
    expect(field("Region")).toHaveValue("eu-south-1");
  });

  it("asks for a profile by name when the computer has none", async () => {
    mockFetch({ [PROFILES]: () => json({ profiles: [] }) });
    renderI18n(
      <AddInstanceDialog engine={ENGINE} existing={[]} onClose={() => {}} onSaved={() => {}} />,
    );
    const profile = await screen.findByRole("textbox", { name: "AWS profile" });
    await waitFor(() => expect(profile).toBeEnabled());
    expect(profile).toHaveAccessibleDescription(
      "No AWS profiles on this computer: create one with aws configure (or aws configure sso), then type its name.",
    );
    expect(field("Region")).toHaveValue("us-east-1");

    // Discovery checks profile and region first.
    await userEvent.clear(field("Region"));
    await userEvent.type(field("Region"), "europe");
    await discover();
    expect(profile).toHaveAttribute("aria-invalid", "true");
    expect(profile).toHaveAccessibleDescription(/Choose an AWS profile\.$/);
    expect(field("Region")).toHaveAccessibleDescription("Enter a region such as us-east-1.");
    expect(profile).toHaveFocus();
  });

  it("shows why the profiles could not be read and still takes one by hand", async () => {
    mockFetch({
      [PROFILES]: () => text("AWS profiles: open ~/.aws/config: permission denied\n", 500),
    });
    renderI18n(
      <AddInstanceDialog engine={ENGINE} existing={[]} onClose={() => {}} onSaved={() => {}} />,
    );
    expect(await within(dialog()).findByRole("alert")).toHaveTextContent(
      /^AWS profiles: open ~\/\.aws\/config: permission denied$/,
    );
    expect(screen.getByRole("textbox", { name: "AWS profile" })).toBeEnabled();
  });

  it("discovers the instances and fills the target from the chosen one", async () => {
    const answer = deferred<void>();
    const { fetch } = open({
      [INSTANCES]: async () => {
        await answer.promise;
        return discovery(
          discovered("sales-sqlserver"),
          discovered("sales-prod-sqlserver", {
            masterUser: "rdsadmin",
            backupRestore: false,
          }),
        );
      },
    });
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );

    await discover();
    const busy = screen.getByRole("button", { name: "Discovering…" });
    expect(busy).toHaveAttribute("aria-busy", "true");
    expect(discoveries(fetch)).toEqual(["?profile=default&region=eu-west-1"]);

    answer.resolve();
    expect(
      await screen.findByText(`Account ${ACCOUNT}. SQL Server instances found: 2.`),
    ).toBeInTheDocument();
    // Two instances: nothing is chosen for the user.
    expect(screen.queryByRole("textbox", { name: "S3 bucket" })).toBeNull();

    await userEvent.click(screen.getByRole("radio", { name: /^sales-prod-sqlserver/ }));
    expect(
      screen.getByText(
        "The option group has no SQLSERVER_BACKUP_RESTORE: import and export fail until it is added.",
      ),
    ).toBeInTheDocument();
    expect(field("SQL login")).toHaveValue("rdsadmin");
    expect(field("Target name")).toHaveValue("sales-prod-sqlserver");
    expect(screen.getByRole("checkbox", { name: "Protected" })).toBeChecked();

    await userEvent.click(screen.getByRole("radio", { name: /^sales-sqlserver/ }));
    expect(field("SQL login")).toHaveValue("admin");
    expect(field("Target name")).toHaveValue("sales-sqlserver");
    expect(screen.getByRole("checkbox", { name: "Protected" })).not.toBeChecked();
    expect(screen.queryByText(/The option group has no SQLSERVER_BACKUP_RESTORE/)).toBeNull();
  });

  it("chooses the only instance found, and keeps what the user typed", async () => {
    open({ [INSTANCES]: found(discovered("my-sqlserver")) });
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );
    await discover();
    expect(await screen.findByRole("radio", { name: /^my-sqlserver/ })).toBeChecked();

    await userEvent.clear(field("Target name"));
    await userEvent.type(field("Target name"), "dev-mssql");
    await discover();
    await waitFor(() => expect(screen.getByRole("radio", { name: /^my-sqlserver/ })).toBeChecked());
    expect(field("Target name")).toHaveValue("dev-mssql");
  });

  it("says when the profile sees no instance, or why discovery failed", async () => {
    open({
      [INSTANCES]: (_req, call) =>
        call === 0
          ? discovery()
          : text("operation error RDS: DescribeDBInstances, AccessDenied\n", 502),
    });
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );
    await discover();
    expect(
      await screen.findByText("This profile sees no RDS SQL Server instance in eu-west-1."),
    ).toBeInTheDocument();

    await discover();
    expect(await within(dialog()).findByRole("alert")).toHaveTextContent(
      /^operation error RDS: DescribeDBInstances, AccessDenied$/,
    );
  });

  it("marks every invalid field, focuses the first one and saves nothing", async () => {
    const { fetch } = open({ [INSTANCES]: found(discovered("my-sqlserver")) }, [DEV.name]);
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );

    // Without an instance the error sends the focus to Discover.
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByText("Choose an instance.")).toBeInTheDocument();
    expect(screen.getByText("Check the highlighted fields.")).toHaveAttribute("role", "alert");
    expect(screen.getByRole("button", { name: "Discover instances" })).toHaveFocus();

    await discover();
    await screen.findByRole("textbox", { name: "S3 bucket" });
    await userEvent.click(screen.getByRole("radio", { name: "Environment variable" }));
    await userEvent.clear(field("Local port"));
    await userEvent.type(field("Local port"), "70000");
    await userEvent.clear(field("Target name"));
    await userEvent.type(field("Target name"), DEV.name);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(field("S3 bucket")).toHaveAttribute("aria-invalid", "true");
    expect(field("S3 bucket")).toHaveAccessibleDescription(
      /Enter a bucket name: lower case letters, digits, dots and -\.$/,
    );
    expect(field("S3 bucket")).toHaveFocus();
    expect(field("Variable name")).toHaveAccessibleDescription(
      "Enter a variable name: letters, digits and _, not starting with a digit.",
    );
    expect(field("Local port")).toHaveAccessibleDescription("Enter a port from 1 to 65535.");
    expect(field("Target name")).toHaveAccessibleDescription(
      /A target has this name: choose another one, or replace it\.$/,
    );
    expect(field("SQL login")).not.toHaveAttribute("aria-invalid");
    expect(saved(fetch)).toEqual([]);

    // Fixing a field clears its message at once.
    await userEvent.type(field("S3 bucket"), "my-sqlserver-backups");
    expect(field("S3 bucket")).not.toHaveAttribute("aria-invalid");
    await userEvent.click(
      screen.getByRole("checkbox", { name: `Replace the existing target ${DEV.name}` }),
    );
    expect(field("Target name")).not.toHaveAttribute("aria-invalid");
  });

  it("saves the target with a password reference only, then reports it", async () => {
    const answer = deferred<void>();
    const { fetch, onSaved, onClose } = open({
      [INSTANCES]: found(discovered("my-sqlserver")),
      [SAVE]: async () => {
        await answer.promise;
        return json({ ...DEV, name: "my-sqlserver" }, 201);
      },
    });
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );
    await discover();
    await userEvent.type(
      await screen.findByRole("textbox", { name: "S3 bucket" }),
      "my-sqlserver-backups",
    );
    await userEvent.click(screen.getByRole("radio", { name: "Environment variable" }));
    await userEvent.type(field("Variable name"), "DEV_SQL_PASSWORD");
    await userEvent.click(screen.getByRole("radio", { name: "Direct" }));
    expect(within(dialog()).queryByRole("textbox", { name: "Local port" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
    expect(saved(fetch)).toEqual([
      {
        name: "my-sqlserver",
        accountId: ACCOUNT,
        region: "eu-west-1",
        profile: "default",
        instance: "my-sqlserver",
        bucket: "my-sqlserver-backups",
        sqlUser: "admin",
        passwordSource: "env:DEV_SQL_PASSWORD",
        connectMode: "direct",
        protected: false,
      },
    ]);

    answer.resolve();
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith({ ...DEV, name: "my-sqlserver" }));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows the engine's refusal and lets the user try again", async () => {
    const { onSaved } = open({
      [INSTANCES]: found(discovered("my-sqlserver")),
      [SAVE]: () => text('target "my-sqlserver" exists: choose another name, or replace it\n', 409),
    });
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );
    await discover();
    await userEvent.type(
      await screen.findByRole("textbox", { name: "S3 bucket" }),
      "my-sqlserver-backups",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    const footer = screen.getByRole("button", { name: "Save" }).closest("footer") as HTMLElement;
    expect(await within(footer).findByRole("alert")).toHaveTextContent(
      'target "my-sqlserver" exists: choose another name, or replace it',
    );
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it.each([
    [
      "the close button",
      () => userEvent.click(within(dialog()).getAllByRole("button", { name: "Cancel" })[0]),
    ],
    [
      "Cancel",
      () => userEvent.click(within(dialog()).getAllByRole("button", { name: "Cancel" })[1]),
    ],
    ["Escape", () => fireEvent(dialog(), new Event("cancel", { cancelable: true }))],
  ])("closes with %s", async (_name, close) => {
    const { onClose } = open({});
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );
    await close();
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

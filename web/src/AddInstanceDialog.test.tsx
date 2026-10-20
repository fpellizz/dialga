import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AddInstanceDialog } from "./AddInstanceDialog";
import type { DiscoveredInstance, InstanceDiscovery, TargetInput } from "./api";
import { DEV, discovered, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, type FetchMock, json, mockFetch, type Route, text } from "./test/http";

const PROFILES = "GET /api/v1/discovery/profiles";
const INSTANCES = "GET /api/v1/discovery/instances";
const BUCKETS = "GET /api/v1/discovery/buckets";
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
  const fetch = mockFetch({
    [PROFILES]: () => json({ profiles: PROFILE_LIST }),
    [BUCKETS]: () => json({ buckets: [] }),
    ...routes,
  });
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

function bucket() {
  return within(dialog()).getByRole("combobox", { name: "S3 bucket" });
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
    await waitFor(() => expect(profile).toHaveFocus());
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
    expect(within(dialog()).queryByRole("combobox", { name: "S3 bucket" })).toBeNull();
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
    // The button says it; the instance list it fills is what is busy.
    expect(screen.getByRole("button", { name: "Discovering…" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Instance" })).toHaveAttribute("aria-busy", "true");
    expect(discoveries(fetch)).toEqual(["?profile=default&region=eu-west-1"]);

    answer.resolve();
    expect(
      await screen.findByText(`Account ${ACCOUNT}. SQL Server instances found: 2.`),
    ).toBeInTheDocument();
    // Two instances: nothing is chosen for the user.
    expect(screen.queryByRole("combobox", { name: "S3 bucket" })).toBeNull();

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
    await userEvent.type(field("Target name"), "dev-sql");
    await discover();
    await waitFor(() => expect(screen.getByRole("radio", { name: /^my-sqlserver/ })).toBeChecked());
    expect(field("Target name")).toHaveValue("dev-sql");
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
    // The engine's message first, then what to do (ErrorBanner).
    expect(await within(dialog()).findByRole("alert")).toHaveTextContent(
      /^operation error RDS: DescribeDBInstances, AccessDenied/,
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
    await screen.findByRole("combobox", { name: "S3 bucket" });
    await userEvent.click(screen.getByRole("radio", { name: "Environment variable" }));
    // The integrated tunnel is the default: a port is asked for an open tunnel only.
    expect(screen.getByRole("radio", { name: "Integrated tunnel" })).toBeChecked();
    expect(within(dialog()).queryByRole("textbox", { name: "Local port" })).toBeNull();
    await userEvent.click(screen.getByRole("radio", { name: "Open tunnel" }));
    expect(field("Local port")).toHaveValue("11433");
    await userEvent.clear(field("Local port"));
    await userEvent.type(field("Local port"), "70000");
    await userEvent.clear(field("Target name"));
    await userEvent.type(field("Target name"), DEV.name);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(bucket()).toHaveAttribute("aria-invalid", "true");
    expect(bucket()).toHaveAccessibleDescription(
      /Enter a bucket name: lower case letters, digits, dots and -\.$/,
    );
    expect(bucket()).toHaveFocus();
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
    await userEvent.type(bucket(), "my-sqlserver-backups");
    expect(bucket()).not.toHaveAttribute("aria-invalid");
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
      await screen.findByRole("combobox", { name: "S3 bucket" }),
      "my-sqlserver-backups",
    );
    await userEvent.click(screen.getByRole("radio", { name: "Environment variable" }));
    await userEvent.type(field("Variable name"), "DEV_SQL_PASSWORD");
    await userEvent.click(screen.getByRole("radio", { name: "Direct" }));
    expect(within(dialog()).queryByRole("textbox", { name: "Local port" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    // Busy, not disabled: the focus stays on the button.
    expect(screen.getByRole("button", { name: "Saving…" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
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
      await screen.findByRole("combobox", { name: "S3 bucket" }),
      "my-sqlserver-backups",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    const footer = screen
      .getByRole("button", { name: "Save" })
      .closest(".dialog-foot") as HTMLElement;
    expect(await within(footer).findByRole("alert")).toHaveTextContent(
      'target "my-sqlserver" exists: choose another name, or replace it',
    );
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it.each([
    [
      "the close button",
      () => userEvent.click(within(dialog()).getByRole("button", { name: "Close" })),
    ],
    ["Cancel", () => userEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }))],
    ["Escape", () => fireEvent(dialog(), new Event("cancel", { cancelable: true }))],
  ])("closes with %s", async (_name, close) => {
    const { onClose } = open({});
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
    );
    await close();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  describe("bucket picker", () => {
    const BUCKET_LIST = ["alpha-backups", "beta-backups", "gamma-exports"].map((name) => ({
      name,
      region: "eu-west-1",
    }));

    /** The options of the bucket list; none while it is closed. */
    function bucketOptions() {
      const list = within(dialog()).queryByRole("listbox", { name: "S3 bucket" });
      return list ? within(list).queryAllByRole("option") : [];
    }

    async function openBucketStep(routes: Record<string, Route> = {}) {
      const opened = open({ [INSTANCES]: found(discovered("my-sqlserver")), ...routes });
      await waitFor(() =>
        expect(screen.getByRole("combobox", { name: "AWS profile" })).toHaveValue("default"),
      );
      await discover();
      await screen.findByRole("combobox", { name: "S3 bucket" });
      return opened;
    }

    it("lists the buckets of the profile and region and picks one", async () => {
      const { fetch } = await openBucketStep({ [BUCKETS]: () => json({ buckets: BUCKET_LIST }) });
      expect(
        await screen.findByText("3 bucket(s) in eu-west-1: choose one or type the name."),
      ).toBeInTheDocument();
      const request = new URL(
        String(fetch.mock.calls.find(([u]) => String(u).includes("/buckets"))?.[0]),
      );
      expect(request.searchParams.get("profile")).toBe("default");
      expect(request.searchParams.get("region")).toBe("eu-west-1");

      expect(bucket()).toHaveAttribute("aria-expanded", "false");
      await userEvent.click(within(dialog()).getByRole("button", { name: "Show the buckets" }));
      expect(bucket()).toHaveAttribute("aria-expanded", "true");
      expect(bucketOptions().map((o) => o.textContent)).toEqual([
        "alpha-backups",
        "beta-backups",
        "gamma-exports",
      ]);
      await userEvent.click(
        bucketOptions().find((o) => o.textContent === "beta-backups") as HTMLElement,
      );
      expect(bucket()).toHaveValue("beta-backups");
      expect(bucket()).toHaveAttribute("aria-expanded", "false");
      expect(bucket()).toHaveFocus();
      // The picked bucket is the selected option when the list opens again.
      await userEvent.click(within(dialog()).getByRole("button", { name: "Show the buckets" }));
      expect(bucketOptions().map((o) => o.getAttribute("aria-selected"))).toEqual([
        "false",
        "true",
        "false",
      ]);
    });

    it("filters while typing, keeps a typed name and works by keyboard", async () => {
      await openBucketStep({ [BUCKETS]: () => json({ buckets: BUCKET_LIST }) });
      await screen.findByText(/3 bucket\(s\)/);
      await userEvent.type(bucket(), "BACKUPS");
      expect(bucketOptions().map((o) => o.textContent)).toEqual(["alpha-backups", "beta-backups"]);
      await userEvent.keyboard("{ArrowDown}{ArrowDown}");
      expect(bucket()).toHaveAttribute("aria-activedescendant", bucketOptions()[1].id);
      await userEvent.keyboard("{Escape}");
      // The first Escape closes the list, not the dialog.
      expect(bucket()).toHaveAttribute("aria-expanded", "false");
      expect(dialog()).toHaveAttribute("open");
      expect(bucket()).toHaveValue("BACKUPS");

      await userEvent.clear(bucket());
      await userEvent.type(bucket(), "not-listed");
      expect(bucketOptions()).toHaveLength(0);
      expect(
        screen.getByText("No bucket matches: the name you typed is kept."),
      ).toBeInTheDocument();
      expect(bucket()).toHaveValue("not-listed");

      await userEvent.clear(bucket());
      await userEvent.type(bucket(), "gamma{ArrowDown}{Enter}");
      expect(bucket()).toHaveValue("gamma-exports");
    });

    it("is busy while the list loads and still accepts a typed name", async () => {
      const list = deferred<Response>();
      await openBucketStep({ [BUCKETS]: () => list.promise });
      expect(screen.getByText("Loading the buckets of eu-west-1…")).toBeInTheDocument();
      expect(bucket().closest(".form-field")).toHaveAttribute("aria-busy", "true");
      await userEvent.type(bucket(), "typed-early");
      list.resolve(json({ buckets: BUCKET_LIST }));
      await waitFor(() =>
        expect(bucket().closest(".form-field")).toHaveAttribute("aria-busy", "false"),
      );
      expect(bucket()).toHaveValue("typed-early");
    });

    it("falls back to typing, with the reason, when the AWS user cannot list buckets", async () => {
      await openBucketStep({
        [BUCKETS]: () =>
          text("the AWS user cannot list buckets (s3:ListAllMyBuckets): type the bucket name", 403),
      });
      const denied = await screen.findByText(
        "The AWS user cannot list buckets (s3:ListAllMyBuckets): type the bucket name.",
      );
      expect(denied).toHaveAttribute("role", "status");
      expect(bucket()).toHaveAccessibleDescription(/s3:ListAllMyBuckets/);
      expect(within(dialog()).queryByRole("alert")).toBeNull();
      expect(within(dialog()).queryByRole("button", { name: "Show the buckets" })).toBeNull();
      await userEvent.type(bucket(), "typed-backups");
      expect(bucket()).toHaveValue("typed-backups");
    });

    it("shows other failures in an alert and keeps the field free", async () => {
      await openBucketStep({ [BUCKETS]: () => text("SSO session expired", 502) });
      expect(await within(dialog()).findByRole("alert")).toHaveTextContent("SSO session expired");
      expect(
        screen.getByText("The bucket list is not available: type the bucket name."),
      ).toBeInTheDocument();
      await userEvent.type(bucket(), "typed-backups");
      expect(bucket()).toHaveValue("typed-backups");
    });

    it("still validates what is typed or picked", async () => {
      await openBucketStep({ [BUCKETS]: () => json({ buckets: BUCKET_LIST }) });
      await userEvent.type(bucket(), "Bad_Name");
      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      expect(bucket()).toHaveAttribute("aria-invalid", "true");
      expect(bucket()).toHaveFocus();
      expect(bucket()).toHaveAccessibleDescription(/Enter a bucket name/);
    });
  });
});

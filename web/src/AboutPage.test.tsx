import { invoke, isTauri } from "@tauri-apps/api/core";
import { fireEvent, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AboutPage, REPO_URL } from "./AboutPage";
import { renderI18n } from "./test/fixtures";
import { ENGINE } from "./test/http";

vi.mock("@tauri-apps/api/core", () => ({ invoke: vi.fn(), isTauri: vi.fn(() => false) }));

afterEach(() => {
  vi.mocked(invoke).mockReset();
  vi.mocked(isTauri).mockReturnValue(false);
});

describe("AboutPage", () => {
  it("shows the versions, the author and the repository link", () => {
    renderI18n(<AboutPage engine={ENGINE} />);
    expect(screen.getByRole("heading", { level: 1, name: "About" })).toBeInTheDocument();
    expect(screen.getByText("Engine").nextElementSibling).toHaveTextContent("0.1.0-test");
    expect(screen.getByText("Local API").nextElementSibling).toHaveTextContent(
      `v1 · ${ENGINE.url}`,
    );
    expect(screen.getByText("Author").nextElementSibling).toHaveTextContent("Fabio Pellizzaro");
    const repo = screen.getByRole("link", { name: /github\.com/ });
    expect(repo).toHaveAttribute("href", expect.stringMatching(/^https:\/\/github\.com\//));
    expect(repo).toHaveAttribute("target", "_blank");
    expect(repo).toHaveAttribute("rel", "noreferrer");
  });

  it("shows a dash while the engine is not there", () => {
    renderI18n(<AboutPage engine={null} />);
    expect(screen.getByText("Engine").nextElementSibling).toHaveTextContent("—");
    expect(screen.getByText("Local API").nextElementSibling).toHaveTextContent("—");
  });

  it("speaks Italian", () => {
    renderI18n(<AboutPage engine={null} />, "it");
    expect(screen.getByRole("heading", { level: 1, name: "Informazioni" })).toBeInTheDocument();
    expect(screen.getByText("Autore")).toBeInTheDocument();
  });

  it("links to the GitHub repository", () => {
    renderI18n(<AboutPage engine={null} />);
    expect(REPO_URL).toBe("https://github.com/fpellizz/dialga");
    fireEvent.click(screen.getByRole("link", { name: REPO_URL }));
    expect(invoke).not.toHaveBeenCalled();
  });

  it("asks the desktop shell to open the repository in the browser", () => {
    vi.mocked(isTauri).mockReturnValue(true);
    renderI18n(<AboutPage engine={null} />);
    const click = fireEvent.click(screen.getByRole("link", { name: REPO_URL }));
    expect(click).toBe(false); // default prevented: the webview does not navigate
    expect(invoke).toHaveBeenCalledWith("open_repository");
  });
});

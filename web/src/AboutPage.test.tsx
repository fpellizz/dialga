import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AboutPage } from "./AboutPage";
import { renderI18n } from "./test/fixtures";
import { ENGINE } from "./test/http";

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
});

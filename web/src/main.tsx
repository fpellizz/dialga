import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles/ds-tokens.css";
import "./styles/app.css";

// Follow the system theme: the Design System's dark tokens hang off data-theme="dark".
const darkQuery = window.matchMedia("(prefers-color-scheme: dark)");
const applyTheme = () => {
  document.documentElement.dataset.theme = darkQuery.matches ? "dark" : "light";
};
applyTheme();
darkQuery.addEventListener("change", applyTheme);

const root = document.getElementById("root");
if (!root) {
  throw new Error("Missing #root element");
}
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

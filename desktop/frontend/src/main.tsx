import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import ErrorBoundary from "./components/ErrorBoundary";
import { DialogProvider } from "./components/dialogs/DialogProvider";
import { I18nProvider } from "./lib/i18n";
import "@fontsource/jetbrains-mono/400.css";
import "@fontsource/jetbrains-mono/700.css";
import "./style.css";
import { loadTheme } from "./theme";
import "./lib/monaco";

loadTheme();

// Document shutdown gate: warn when unsaved editor changes would be lost.
// The WorkspaceNavigator sets window.__kitodock_dirtyDocs.
window.addEventListener("beforeunload", (event) => {
  const dirty = (window as any).__kitodock_dirtyDocs ?? 0;
  if (dirty > 0) {
    event.preventDefault();
    event.returnValue = "";
  }
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <I18nProvider>
        <DialogProvider>
          <App />
        </DialogProvider>
      </I18nProvider>
    </ErrorBoundary>
  </React.StrictMode>
);

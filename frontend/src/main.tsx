// File overview: React entrypoint. It mounts the root App component into the static shell served by Go.

import * as React from "react";
import { StrictMode } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import "@fontsource/fraunces/latin-700.css";
import App from "./App";
import { installRolltopPluginReactRuntime } from "./plugins/shared/reactRuntime";
import "./styles.scss";

installRolltopPluginReactRuntime({ React, ReactDOM: { createPortal } });

createRoot(document.getElementById("root") as HTMLElement).render(
  <StrictMode>
    <App />
  </StrictMode>
);

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker
      .register("/sw.js")
      .then((registration) => registration.update())
      .catch(() => {
        // The app still works without the offline cache.
      });
  });
}

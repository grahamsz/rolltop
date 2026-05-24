import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource/fraunces/latin-700.css";
import App from "./App";
import "./styles.scss";

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

// File overview: Vite build configuration for the React frontend bundle that the Go server serves.

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  root: "frontend",
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: true
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/attachments": "http://127.0.0.1:8080",
      "/blobs": "http://127.0.0.1:8080",
      "/plugins": "http://127.0.0.1:8080"
    }
  }
});

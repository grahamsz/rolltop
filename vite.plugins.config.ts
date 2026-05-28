// File overview: Vite library builds for runtime-loaded frontend plugin bundles.

import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const fromRoot = (path: string) => fileURLToPath(new URL(path, import.meta.url));

export default defineConfig({
  root: ".",
  plugins: [react()],
  define: {
    "process.env.NODE_ENV": JSON.stringify("production")
  },
  resolve: {
    alias: [
      { find: /^react$/, replacement: fromRoot("./frontend/src/plugins/shared/reactShim.ts") },
      { find: /^react-dom$/, replacement: fromRoot("./frontend/src/plugins/shared/reactDOMShim.ts") },
      { find: /^react\/jsx-runtime$/, replacement: fromRoot("./frontend/src/plugins/shared/reactJSXRuntimeShim.ts") },
      { find: /^react\/jsx-dev-runtime$/, replacement: fromRoot("./frontend/src/plugins/shared/reactJSXRuntimeShim.ts") }
    ]
  },
  build: {
    outDir: "plugins/client_side_pgp/frontend_dist",
    emptyOutDir: true,
    sourcemap: true,
    lib: {
      entry: "plugins/client_side_pgp/frontend/index.ts",
      formats: ["es"],
      fileName: () => "index.js"
    },
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
        entryFileNames: "index.js",
        chunkFileNames: "chunks/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]"
      }
    }
  }
});

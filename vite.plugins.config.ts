// File overview: Vite library builds for runtime-loaded frontend plugin bundles.

import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const fromRoot = (path: string) => fileURLToPath(new URL(path, import.meta.url));

const target = (process.env.ROLLTOP_PLUGIN_TARGET || "client_side_pgp").trim();
const pluginConfig: Record<string, { entry: string; outDir: string }> = {
  attachment_preview: {
    entry: "plugins/attachment_preview/frontend/index.tsx",
    outDir: "plugins/attachment_preview/frontend_dist"
  },
  client_side_pgp: {
    entry: "plugins/client_side_pgp/frontend/index.ts",
    outDir: "plugins/client_side_pgp/frontend/dist"
  },
  gravatar_sender_icons: {
    entry: "plugins/gravatar_sender_icons/frontend/index.ts",
    outDir: "plugins/gravatar_sender_icons/frontend_dist"
  },
  bimi_brand_icons: {
    entry: "plugins/bimi_brand_icons/frontend/index.ts",
    outDir: "plugins/bimi_brand_icons/frontend_dist"
  },
  language_search: {
    entry: "plugins/language_search/frontend/index.ts",
    outDir: "plugins/language_search/frontend_dist"
  },
  one_click_unsubscribe: {
    entry: "plugins/one_click_unsubscribe/frontend/index.tsx",
    outDir: "plugins/one_click_unsubscribe/frontend_dist"
  },
  remote_image_blocklist: {
    entry: "plugins/remote_image_blocklist/frontend/index.tsx",
    outDir: "plugins/remote_image_blocklist/frontend_dist"
  },
  trusted_image_sources: {
    entry: "plugins/trusted_image_sources/frontend/index.tsx",
    outDir: "plugins/trusted_image_sources/frontend_dist"
  },
  matrix_theme: {
    entry: "plugins/matrix_theme/frontend/index.ts",
    outDir: "plugins/matrix_theme/frontend_dist"
  },
  mail_filters: {
    entry: "plugins/mail_filters/frontend/index.tsx",
    outDir: "plugins/mail_filters/frontend_dist"
  },
  mail_mcp: {
    entry: "plugins/mail_mcp/frontend/index.tsx",
    outDir: "plugins/mail_mcp/frontend_dist"
  }
};

const selected = pluginConfig[target];
if (!selected) {
  throw new Error(`Unknown plugin build target: ${target}`);
}

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
    outDir: selected.outDir,
    emptyOutDir: true,
    sourcemap: true,
    lib: {
      entry: selected.entry,
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

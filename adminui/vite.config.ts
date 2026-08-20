import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

const adminProxyTarget = process.env.ADMIN_UI_PROXY_TARGET ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [svelte()],
  publicDir: false,
  server: {
    proxy: {
      "/api": {
        target: adminProxyTarget,
        changeOrigin: true,
      },
      "/healthz": {
        target: adminProxyTarget,
        changeOrigin: true,
      },
      "/readyz": {
        target: adminProxyTarget,
        changeOrigin: true,
      },
      "/metrics": {
        target: adminProxyTarget,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    cssCodeSplit: false,
    sourcemap: false,
    target: "es2019",
    rollupOptions: {
      input: "src/main.ts",
      output: {
        entryFileNames: "app.js",
        chunkFileNames: "app.js",
        assetFileNames: (assetInfo) =>
          assetInfo.name && assetInfo.name.endsWith(".css")
            ? "styles.css"
            : "assets/[name][extname]",
        inlineDynamicImports: true,
      },
    },
  },
});

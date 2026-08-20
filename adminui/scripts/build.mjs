import { build } from "vite";
import { copyFile, mkdir, rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const uiRoot = path.resolve(__dirname, "..");
const distDir = path.join(uiRoot, "dist");
const productionIndexPath = path.join(uiRoot, "index.prod.html");
const assetsDir =
  process.env.ADMIN_UI_ASSETS_DIR ??
  path.resolve(uiRoot, "..", "pkg", "adminui", "assets");

await build({
  configFile: path.join(uiRoot, "vite.config.ts"),
});

await rm(assetsDir, { recursive: true, force: true });
await mkdir(assetsDir, { recursive: true });

await copyFile(path.join(distDir, "app.js"), path.join(assetsDir, "app.js"));
await copyFile(path.join(distDir, "styles.css"), path.join(assetsDir, "styles.css"));
await copyFile(productionIndexPath, path.join(assetsDir, "index.html"));

import fs from "node:fs/promises";
import path from "node:path";

const { Presentation, PresentationFile, image, fill } = await import("@oai/artifact-tool");

const pageDir = process.env.GUIDE_PAGES_DIR;
const deckTitle = process.env.GUIDE_PPTX_TITLE || "Tunnel End-User Guide";

if (!pageDir) {
  throw new Error("GUIDE_PAGES_DIR is required");
}

const PAGE_WIDTH = 1200;
const PAGE_HEIGHT = 1553;

const pageTitleOverrides = new Map([
  ["01-cover.png", "Cover"],
  ["02-what-tunnel-client-does.png", "What tunnel-client does"],
  ["03-before-you-start-links.png", "Before you start"],
  ["04-key-values-and-permissions.png", "Key values and permissions"],
  ["05-roles-and-groups.png", "Roles and groups"],
  ["06-create-the-tunnel.png", "Create the tunnel"],
  ["07-first-success-terminal.png", "First success in the terminal"],
  ["08-profiles-and-readiness.png", "Profiles and readiness"],
  ["09-local-ui-overview.png", "Local UI overview"],
  ["10-local-ui-logs-and-codex.png", "Logs and Codex"],
  ["11-connect-chatgpt.png", "Connect ChatGPT"],
  ["12-codex-commands.png", "Codex commands"],
  ["13-starter-phrases.png", "Starter phrases"],
  ["14-faq-and-companion-docs.png", "FAQ and companion docs"],
]);

function titleFromFilename(filename) {
  if (pageTitleOverrides.has(filename)) {
    return pageTitleOverrides.get(filename);
  }
  return filename
    .replace(/^\d+-/, "")
    .replace(/\.png$/i, "")
    .split("-")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

const pageEntries = (await fs.readdir(pageDir))
  .filter((entry) => entry.endsWith(".png"))
  .sort();

if (!pageEntries.length) {
  throw new Error(`No page PNGs found under ${pageDir}`);
}

const presentation = Presentation.create({
  metadata: {
    title: deckTitle,
  },
  slideSize: { width: PAGE_WIDTH, height: PAGE_HEIGHT },
});

for (const entry of pageEntries) {
  const absolutePath = path.join(pageDir, entry);
  const slide = presentation.slides.add();
  slide.compose(
    image({
      name: titleFromFilename(entry),
      path: absolutePath,
      width: fill,
      height: fill,
      fit: "cover",
      alt: titleFromFilename(entry),
    }),
    {
      frame: { left: 0, top: 0, width: PAGE_WIDTH, height: PAGE_HEIGHT },
      baseUnit: 8,
    },
  );
}

const pptxBlob = await PresentationFile.exportPptx(presentation);
await pptxBlob.save("output/output.pptx");

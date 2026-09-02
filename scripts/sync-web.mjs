import { cp, mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const source = resolve(root, "apps/web/dist");
const target = resolve(root, "apps/backend/internal/webui/dist");

const sourceEntries = await readdir(source);
if (!sourceEntries.includes("index.html")) {
  throw new Error(`Refusing to sync: ${source} does not contain index.html`);
}

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(source, target, { recursive: true });
await writeFile(resolve(target, ".gitkeep"), "");

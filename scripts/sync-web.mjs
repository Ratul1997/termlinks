import { cp, mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const source = resolve(root, "apps/web/dist");
const target = resolve(root, "apps/backend/internal/webui/dist");
const functionsSource = resolve(root, "apps/web/functions");
const functionsTarget = resolve(root, "apps/backend/internal/cloudflarepages/bundle/functions");

const sourceEntries = await readdir(source);
if (!sourceEntries.includes("index.html")) {
  throw new Error(`Refusing to sync: ${source} does not contain index.html`);
}

await rm(target, { recursive: true, force: true });
await mkdir(target, { recursive: true });
await cp(source, target, { recursive: true });
await writeFile(resolve(target, ".gitkeep"), "");
await rm(functionsTarget, { recursive: true, force: true });
await mkdir(functionsTarget, { recursive: true });
await cp(functionsSource, functionsTarget, { recursive: true });
await writeFile(resolve(functionsTarget, ".gitkeep"), "");

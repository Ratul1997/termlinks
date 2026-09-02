import { mkdir } from "node:fs/promises";
import { spawn } from "node:child_process";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const outputDir = resolve(root, "dist");
await mkdir(outputDir, { recursive: true });
const linkerFlags = process.platform === "darwin" ? "-linkmode=external -s -w" : "-s -w";

const output = resolve(outputDir, "termlinks");

await run("go", ["build", "-trimpath", "-ldflags", linkerFlags, "-o", output, "./cmd/termlinks"], resolve(root, "apps/backend"));
if (process.platform === "darwin") {
  await run("codesign", ["--force", "--sign", "-", "--identifier", "dev.termlinks.cli", output], root);
}

function run(command, args, cwd) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, { cwd, stdio: "inherit" });
    child.on("error", reject);
    child.on("exit", (code) => code === 0 ? resolvePromise() : reject(new Error(`${command} exited with status ${code ?? "unknown"}`)));
  });
}

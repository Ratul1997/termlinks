import { chmod, copyFile, mkdir, mkdtemp, rename, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const outputDir = resolve(root, "dist");
await mkdir(outputDir, { recursive: true });
const linkerFlags = process.platform === "darwin" ? "-linkmode=external -s -w" : "-s -w";

const output = resolve(outputDir, "termlinks");
const temporaryDir = await mkdtemp(resolve(tmpdir(), "termlinks-build-"));
const stagedOutput = resolve(temporaryDir, "termlinks");
try {
  await run("go", ["build", "-trimpath", "-ldflags", linkerFlags, "-o", stagedOutput, "./cmd/termlinks"], resolve(root, "apps/backend"));
  if (process.platform === "darwin") {
    // Signing directly on some external APFS volumes intermittently fails inside
    // Security.framework. Sign in the system temporary directory, then publish.
    await run("codesign", ["--force", "--sign", "-", "--identifier", "dev.termlinks.cli", stagedOutput], root);
  }
  const nextOutput = `${output}.next`;
  try {
    await copyFile(stagedOutput, nextOutput);
    await chmod(nextOutput, 0o755);
    await rename(nextOutput, output);
  } catch (error) {
    await rm(nextOutput, { force: true });
    if (error?.code !== "ENOSPC") throw error;
    // Low-space development volumes may not have room for two complete build
    // artifacts. The existing dist binary is reproducible, so replace it in place.
    await copyFile(stagedOutput, output);
    await chmod(output, 0o755);
  }
} finally {
  await rm(temporaryDir, { recursive: true, force: true });
}

function run(command, args, cwd) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, { cwd, stdio: "inherit" });
    child.on("error", reject);
    child.on("exit", (code) => code === 0 ? resolvePromise() : reject(new Error(`${command} exited with status ${code ?? "unknown"}`)));
  });
}

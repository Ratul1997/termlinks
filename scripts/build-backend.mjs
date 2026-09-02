import { mkdir } from "node:fs/promises";
import { spawn } from "node:child_process";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const outputDir = resolve(root, "dist");
await mkdir(outputDir, { recursive: true });
const linkerFlags = process.platform === "darwin" ? "-linkmode=external -s -w" : "-s -w";

const child = spawn(
  "go",
  ["build", "-trimpath", "-ldflags", linkerFlags, "-o", resolve(outputDir, "termlinks"), "./cmd/termlinks"],
  { cwd: resolve(root, "apps/backend"), stdio: "inherit" },
);

child.on("exit", (code) => process.exit(code ?? 1));

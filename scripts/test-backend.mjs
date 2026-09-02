import { spawn } from "node:child_process";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const arguments_ = ["test"];
if (process.platform === "darwin") arguments_.push("-ldflags=-linkmode=external");
arguments_.push("./...");
const child = spawn("go", arguments_, {
  cwd: resolve(root, "apps/backend"),
  stdio: "inherit",
});

child.on("exit", (code) => process.exit(code ?? 1));

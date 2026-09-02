import { build, context } from "esbuild";
import { cp, mkdir, rm } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, "dist");
const serving = process.argv.includes("--serve");

await rm(output, { recursive: true, force: true });
await mkdir(resolve(output, "assets"), { recursive: true });
await cp(resolve(root, "index.html"), resolve(output, "index.html"));
await cp(resolve(root, "_routes.json"), resolve(output, "_routes.json"));
await cp(resolve(root, "_headers"), resolve(output, "_headers"));

const options = {
  entryPoints: [resolve(root, "src/main.ts")],
  bundle: true,
  format: "esm",
  target: "es2022",
  outdir: resolve(output, "assets"),
  entryNames: "main",
  assetNames: "[name]-[hash]",
  minify: !serving,
  sourcemap: serving,
  logLevel: "info",
};

if (serving) {
  const builder = await context(options);
  await builder.watch();
  const server = await builder.serve({ servedir: output, host: "127.0.0.1", port: 5173 });
  console.log(`Termlinks web development server: http://${server.host}:${server.port}`);
} else {
  await build(options);
}

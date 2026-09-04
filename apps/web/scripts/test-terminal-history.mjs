import { build } from "esbuild";
import { resolve } from "node:path";

for (const entry of ["terminal-history.test.ts", "terminal-reconnect.test.ts", "passkeys.test.ts"]) {
  const bundled = await build({
    entryPoints: [resolve(import.meta.dirname, `../src/${entry}`)],
    bundle: true,
    write: false,
    format: "esm",
    platform: "node",
    target: "node20",
  });
  const moduleURL = `data:text/javascript;base64,${Buffer.from(bundled.outputFiles[0].contents).toString("base64")}`;
  await import(moduleURL);
}

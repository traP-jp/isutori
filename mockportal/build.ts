import { execFileSync } from "node:child_process";
import { copyFileSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import * as esbuild from "esbuild";
import * as sass from "sass";

const upstream = "https://github.com/isucon/isucon14-portal.git";
const commit = "4c2b99a69b03dc0af56642dcf7ed9ef2015935e4";
const cache = ".cache/isucon14-portal";
const dist = "../internal/mockportal/dist";

const git = (...args: string[]) => execFileSync("git", ["-C", cache, ...args], { encoding: "utf8" }).trim();
if (!existsSync(cache)) {
  execFileSync("git", ["clone", "--filter=blob:none", "--no-checkout", upstream, cache], { stdio: "inherit" });
}
git("checkout", "--quiet", commit);
if (process.argv.includes("--fetch-only")) process.exit(0);

mkdirSync(dist, { recursive: true });
await esbuild.build({
  entryPoints: ["src/main.tsx"],
  bundle: true,
  minify: true,
  target: "es2022",
  outfile: `${dist}/app.js`,
  define: { "process.env.NODE_ENV": '"production"' },
  banner: { js: `/*! Built from isucon/isucon14-portal@${commit}, MIT License, Copyright (c) 2022 ISUCON. See LICENSE.isucon14-portal. */` },
});
const css = sass.compile(`${cache}/portal/app/javascript/packs/application.scss`, {
  loadPaths: ["node_modules"],
  style: "compressed",
  quietDeps: true,
  silenceDeprecations: ["import", "global-builtin", "color-functions", "slash-div", "if-function"],
});
writeFileSync(`${dist}/app.css`, css.css);
copyFileSync(`${cache}/LICENSE`, `${dist}/LICENSE.isucon14-portal`);

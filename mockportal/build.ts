import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
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
const bundle = await esbuild.build({
  entryPoints: ["src/main.tsx"],
  bundle: true,
  minify: true,
  target: "es2022",
  outfile: `${dist}/app.js`,
  define: { "process.env.NODE_ENV": '"production"' },
  banner: { js: `/*! Built from isucon/isucon14-portal@${commit} and npm packages; see THIRD_PARTY_LICENSES. */` },
  metafile: true,
});
const css = sass.compile(`${cache}/portal/app/javascript/packs/application.scss`, {
  loadPaths: ["node_modules"],
  style: "compressed",
  quietDeps: true,
  silenceDeprecations: ["import", "global-builtin", "color-functions", "slash-div", "if-function"],
});
writeFileSync(`${dist}/app.css`, css.css);

const bundledFiles = [...Object.keys(bundle.metafile.inputs), ...css.loadedUrls].map((f) => realpathSync(f));
const packageRoots = new Set(
  bundledFiles.flatMap((f) => f.match(/^(.*\/node_modules\/(?:@[^/]+\/)?[^/]+)\//)?.[1] ?? []),
);
const notices = [...packageRoots].map((root) => {
  const pkg = JSON.parse(readFileSync(`${root}/package.json`, "utf8"));
  const shipped = readdirSync(root).find((f) => /^(licen[cs]e|copying)(\.|$)/i.test(f));
  const licensePath = shipped ? `${root}/${shipped}` : `licenses/${pkg.name}.txt`;
  if (!existsSync(licensePath)) throw new Error(`${pkg.name} ships no license file; add one at ${licensePath}`);
  return `${pkg.name}@${pkg.version}\nLicense: ${pkg.license}\n\n${readFileSync(licensePath, "utf8").trim()}`;
});
notices.sort();
notices.unshift(`isucon/isucon14-portal@${commit}\nLicense: MIT\n\n${readFileSync(`${cache}/LICENSE`, "utf8").trim()}`);
writeFileSync(`${dist}/THIRD_PARTY_LICENSES`, `${notices.join("\n\n----------------------------------------\n\n")}\n`);

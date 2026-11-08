// Copies monaco-editor's min/vs into public/vs so the editor works fully
// offline (no CDN). Run by predev/prebuild hooks.
import { cpSync, existsSync, mkdirSync, writeFileSync, readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
const here = dirname(fileURLToPath(import.meta.url));

const src = join(here, "..", "node_modules", "monaco-editor", "min", "vs");
const dest = join(here, "..", "public", "vs");
const stamp = join(dest, ".version");

const version = JSON.parse(readFileSync(join(here, "..", "node_modules", "monaco-editor", "package.json"), "utf8")).version;

if (existsSync(stamp) && readFileSync(stamp, "utf8").trim() === version) {
  console.log(`monaco ${version} already vendored`);
  process.exit(0);
}
mkdirSync(dest, { recursive: true });
cpSync(src, dest, { recursive: true });
writeFileSync(stamp, version);
console.log(`vendored monaco ${version} -> public/vs`);

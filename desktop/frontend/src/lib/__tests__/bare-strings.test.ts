import { describe, expect, it } from "vitest";

// Meta-test: user-visible JSX text in pages and components must come from
// the i18n catalogue (t("…")) instead of hardcoded English. A debt since
// Phase 0; this keeps new code honest without churning existing files:
// violations fail the run and get migrated key-by-key.
//
// What counts as bare: a JSX text child (or literal attribute like
// title="…") with two or more English-looking words. Allowed exceptions:
// pure technical tokens (paths, URLs, key names, single words, casing
// like CSS/CLI identifiers).

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const ROOTS = ["src/pages", "src/components"];
// Files that predate this rule and still carry known literals; the rule
// is for new code — shrink this list, never grow it.
const GRANDFATHERED = new Set<string>([]);
// Proper nouns that stay untranslated wherever they appear.
const ALLOWED_TERMS = /^(Git Bash|PowerShell|Next\.js|Node\.js|WebView2|Claude Code)$/;

const SKIP_TEXT = /^[^a-z]*$/; // no lowercase letters → technical token
const ENGLISHY = /^[A-Z][a-z]+(?:[ ,'/-][A-Za-z'][a-z]+)+/;

function walk(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) {
      out.push(...walk(full));
    } else if (/\.tsx$/.test(name)) {
      out.push(full);
    }
  }
  return out;
}

function jsxTextLiterals(source: string): { line: number; text: string }[] {
  const hits: { line: number; text: string }[] = [];
  const lines = source.split("\n");
  lines.forEach((line, i) => {
    // JSX text children: `>Some words<` (not inside {…}).
    for (const match of line.matchAll(/>\s*([^<>{}]+?)\s*</g)) {
      const text = match[1].trim();
      if (text !== "" && ENGLISHY.test(text) && !ALLOWED_TERMS.test(text)) {
        hits.push({ line: i + 1, text });
      }
    }
    // Literal title/aria-label/label/placeholder attributes.
    for (const match of line.matchAll(/(?:title|aria-label|label|placeholder)="([^"]+)"/g)) {
      const text = match[1].trim();
      if (ENGLISHY.test(text) && !ALLOWED_TERMS.test(text)) hits.push({ line: i + 1, text });
    }
  });
  return hits;
}

describe("no bare English UI strings", () => {
  it.each(ROOTS)("root %s uses t() for user-visible copy", (root) => {
    const offenders: string[] = [];
    for (const file of walk(root)) {
      if (GRANDFATHERED.has(file)) continue;
      for (const hit of jsxTextLiterals(readFileSync(file, "utf-8"))) {
        offenders.push(`${file}:${hit.line}: ${hit.text}`);
      }
    }
    expect(offenders, offenders.join("\n")).toEqual([]);
  });
});

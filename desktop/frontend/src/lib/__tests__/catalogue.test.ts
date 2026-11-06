// Shipped-catalogue guard: every locale carries exactly the English key set,
// every English {{placeholder}} survives into every translation, and plural
// units always include the "other" form. The Go side runs its own version of
// this contract from internal/i18n/keys_test.go against the code's usage;
// this file covers locale parity and shape.
import { beforeEach, describe, expect, it } from "vitest";
import { catalogues, FALLBACK, localesOf } from "../i18n";

const PLURAL_KEYS = new Set(["zero", "one", "two", "few", "many", "other"]);

function flattenLeaves(
  doc: Record<string, unknown>,
  prefix = "",
  out: Record<string, unknown> = {},
): Record<string, unknown> {
  for (const [key, value] of Object.entries(doc)) {
    if (key === "$meta") continue;
    const path = prefix ? `${prefix}.${key}` : key;
    const isPlural =
      typeof value === "object" &&
      value !== null &&
      !Array.isArray(value) &&
      Object.keys(value).length > 0 &&
      Object.keys(value).every((k) => PLURAL_KEYS.has(k));
    if (typeof value === "object" && value !== null && !Array.isArray(value) && !isPlural) {
      flattenLeaves(value as Record<string, unknown>, path, out);
    } else {
      out[path] = value;
    }
  }
  return out;
}

function placeholdersOf(value: unknown): string[] {
  const text =
    typeof value === "string"
      ? value
      : Object.values(value as Record<string, string>).join(" ");
  return [...text.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();
}

// Locale codes are static at collection time; it.each needs them right away.
const otherLocales = Object.keys(catalogues).filter((code) => code !== FALLBACK);
let englishLeaves: Record<string, unknown>;

beforeEach(() => {
  englishLeaves = flattenLeaves(catalogues[FALLBACK] as Record<string, unknown>);
});

describe("shipped catalogues", () => {
  it("bundle the English fallback and at least one other locale", () => {
    expect(catalogues[FALLBACK]).toBeDefined();
    expect(otherLocales.length).toBeGreaterThan(0);
  });

  it("lists the fallback first", () => {
    expect(localesOf(catalogues)[0].code).toBe(FALLBACK);
  });

  it.each(otherLocales)("%s matches the English key set exactly", (code) => {
    const leaves = flattenLeaves(catalogues[code] as Record<string, unknown>);
    expect(Object.keys(leaves).sort()).toEqual(Object.keys(englishLeaves).sort());
  });

  it.each(otherLocales)("%s keeps every English placeholder", (code) => {
    const leaves = flattenLeaves(catalogues[code] as Record<string, unknown>);
    for (const [key, englishValue] of Object.entries(englishLeaves)) {
      if (typeof englishValue !== "string" && typeof englishValue !== "object") continue;
      expect(placeholdersOf(leaves[key]), `${code}:${key}`).toEqual(placeholdersOf(englishValue));
    }
  });

  it.each(Object.keys(catalogues))("%s plural units keep an 'other' form", (code) => {
    const leaves = flattenLeaves(catalogues[code] as Record<string, unknown>);
    for (const [key, value] of Object.entries(leaves)) {
      if (value && typeof value === "object") {
        expect((value as Record<string, string>).other, `${code}:${key}`).toBeTypeOf("string");
      }
    }
  });
});

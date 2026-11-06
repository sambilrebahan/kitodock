// The pure renderer, tested without React and without the bridge. The same
// rules are implemented in Go by internal/i18n; these pin the frontend half.
import { describe, expect, it } from "vitest";
import { FALLBACK, translate, type Catalogue } from "../i18n";

const packs: Record<string, Catalogue> = {
  en: {
    home: "Home",
    inbox: {
      label: "Queue {{name}}",
      pending: { one: "{{count}} item waiting", other: "{{count}} items waiting" },
    },
  },
  id: {
    home: "Beranda",
  },
};

describe("translate", () => {
  it("renders from the requested catalogue", () => {
    expect(translate(packs, "en", "home")).toBe("Home");
    expect(translate(packs, "id", "home")).toBe("Beranda");
  });

  it("falls back to English, then to the key path", () => {
    expect(translate(packs, "id", "inbox.label", { name: "jobs" })).toBe("Queue jobs");
    expect(translate(packs, "en", "nowhere.key")).toBe("nowhere.key");
  });

  it("interpolates plain values verbatim", () => {
    expect(translate(packs, "en", "inbox.label", { name: "web-7" })).toBe("Queue web-7");
    // A missing variable stays visible rather than silently vanishing.
    expect(translate(packs, "en", "inbox.label")).toBe("Queue {{name}}");
  });

  it("formats counts for the locale and picks the plural form", () => {
    expect(translate(packs, "en", "inbox.pending", { count: 1 })).toBe("1 item waiting");
    expect(translate(packs, "en", "inbox.pending", { count: 5 })).toBe("5 items waiting");
    // 1234 is prose in English: grouped. (Indonesian groups with dots.)
    expect(translate(packs, "en", "inbox.pending", { count: 1234 })).toBe("1,234 items waiting");
  });

  it("treats a plural unit without a count as its 'other' form", () => {
    expect(translate(packs, "en", "inbox.pending")).toBe("{{count}} items waiting");
  });

  it("uses the fixed fallback code", () => {
    expect(FALLBACK).toBe("en");
  });
});

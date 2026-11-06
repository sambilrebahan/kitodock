import clsx from "clsx";
import { useState } from "react";
import { useI18n, useT } from "../lib/i18n";
import { applyTheme, getCurrentTheme, THEMES, type ThemeId } from "../theme";

// Phase 0 Settings: appearance and language. Services, command center,
// experimental and updates join as their phases land
// (docs/03-roadmap-detail.md).
export default function Settings() {
  const t = useT();
  return (
    <div className="mx-auto max-w-[860px] space-y-4">
      <h1 className="text-lg font-semibold text-white/95">{t("settings.title")}</h1>
      <AppearanceSection />
      <LanguageSection />
    </div>
  );
}

function Panel({ title, help, children }: { title: string; help?: string; children: React.ReactNode }) {
  return (
    <section className="glass p-4">
      <h2 className="text-sm font-semibold text-white/90">{title}</h2>
      {help && <p className="mt-1 text-xs leading-relaxed text-white/55">{help}</p>}
      <div className="mt-3">{children}</div>
    </section>
  );
}

function AppearanceSection() {
  const t = useT();
  const [theme, setTheme] = useState<ThemeId>(() => getCurrentTheme());

  const pick = (id: ThemeId) => {
    applyTheme(id);
    setTheme(id);
  };

  return (
    <Panel title={t("settings.appearance.title")} help={t("settings.appearance.themesHelp")}>
      <div className="mb-2 text-xs font-medium text-white/70">{t("settings.appearance.themesLabel")}</div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {THEMES.map((def) => (
          <button
            key={def.id}
            type="button"
            onClick={() => pick(def.id)}
            aria-pressed={theme === def.id}
            className={clsx(
              "flex items-center gap-3 rounded-lg border p-2.5 text-left transition-colors",
              theme === def.id
                ? "border-accent bg-accent/15"
                : "border-white/10 bg-white/[0.03] hover:bg-white/[0.06]",
            )}
          >
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-white/15">
              <span className="h-2 w-2 rounded-full" style={{ background: def.swatch[0] }} />
              <span className="ml-1 h-2 w-2 rounded-full" style={{ background: def.swatch[1] }} />
            </span>
            <span className="min-w-0">
              <span className="block truncate text-[13px] font-medium text-white/90">{def.label}</span>
              <span className="block truncate text-[11px] text-white/50">{t(def.descriptionKey)}</span>
            </span>
          </button>
        ))}
      </div>
    </Panel>
  );
}

function LanguageSection() {
  const t = useT();
  const { lang, locales, setLang } = useI18n();

  return (
    <Panel title={t("settings.language.title")} help={t("settings.language.help")}>
      <select
        className="input max-w-[320px]"
        value={lang}
        onChange={(event) => setLang(event.target.value)}
        aria-label={t("settings.language.title")}
      >
        {locales.map((meta) => (
          <option key={meta.code} value={meta.code}>
            {meta.nativeName} ({meta.name}){meta.autoTranslated ? " · " + t("settings.language.needsReview") : ""}
          </option>
        ))}
      </select>
    </Panel>
  );
}

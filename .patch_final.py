import io
import json
import collections

FE = r"C:\Users\zakyh\Documents\Big Project\kitodock\desktop\frontend"
DK = r"C:\Users\zakyh\Documents\Big Project\kitodock\desktop"

# --- Settings: TuningSection -------------------------------------------------
p = FE + r"\src\pages\Settings.tsx"
src = io.open(p, encoding="utf-8").read()

src = src.replace(
    'import { LocalCAStatus, TrustLocalCA } from "../../wailsjs/go/controllers/Projects";',
    'import { GetTuning, LocalCAStatus, SetTuning, TrustLocalCA } from "../../wailsjs/go/controllers/Projects";',
    1,
)
src = src.replace(
    "      <ExperimentalSection />\n      <UpdatesSection />\n      <DiagnosticsSection />",
    "      <ExperimentalSection />\n      <UpdatesSection />\n      <TuningSection />\n      <DiagnosticsSection />",
    1,
)

src += '''

type TuningCard = { server: string; values?: Record<string, string>; available?: string[] };

function TuningSection() {
  const t = useT();
  const dialogs = useDialogs();
  const [cards, setCards] = useState<TuningCard[]>([]);
  const [drafts, setDrafts] = useState<Record<string, Record<string, string>>>({});
  const [busy, setBusy] = useState("");

  const load = useCallback(() => {
    GetTuning()
      .then((list) => {
        setCards(list ?? []);
        const next: Record<string, Record<string, string>> = {};
        for (const card of list ?? []) {
          next[card.server] = { ...(card.values ?? {}) };
        }
        setDrafts(next);
      })
      .catch(() => setCards([]));
  }, []);

  useEffect(() => {
    const timer = setTimeout(() => load(), 0);
    return () => clearTimeout(timer);
  }, [load]);

  const save = async (server: string, directive: string) => {
    const value = (drafts[server] ?? {})[directive] ?? "";
    if (value === ((cards.find((c) => c.server === server)?.values ?? {})[directive] ?? "")) {
      return; // unchanged
    }
    setBusy(server + directive);
    try {
      await SetTuning(server, directive, value);
      load();
    } catch (error) {
      await dialogs.alert({ title: server, message: String(error) });
    } finally {
      setBusy("");
    }
  };

  return (
    <Panel title={t("settings.tuning.title")} help={t("settings.tuning.help")}>
      {cards.length === 0 ? (
        <p className="text-xs text-white/40">{t("settings.tuning.empty")}</p>
      ) : (
        cards.map((card) => (
          <div key={card.server} className="mb-3 last:mb-0 border-t border-white/5 pt-3 first:border-t-0 first:pt-0">
            <p className="mb-2 text-xs font-medium text-white/80">
              {card.server === "nginx" ? "nginx" : "PHP " + card.server.replace("php-", "")}
            </p>
            <div className="grid grid-cols-2 gap-2">
              {(card.available ?? []).map((directive) => (
                <label key={directive} className="flex items-center gap-2 text-[11px]">
                  <span className="w-44 shrink-0 truncate font-mono text-white/55" title={directive}>
                    {directive}
                  </span>
                  <input
                    className="input min-w-0 flex-1 py-1 font-mono text-[11px]"
                    value={(drafts[card.server] ?? {})[directive] ?? ""}
                    placeholder={t("settings.tuning.default")}
                    onChange={(e) =>
                      setDrafts((prev) => ({
                        ...prev,
                        [card.server]: { ...(prev[card.server] ?? {}), [directive]: e.target.value },
                      }))
                    }
                    onKeyDown={(e) => {
                      if (e.key === "Enter") void save(card.server, directive);
                    }}
                  />
                  <button
                    type="button"
                    disabled={busy === card.server + directive}
                    onClick={() => void save(card.server, directive)}
                    className="shrink-0 rounded bg-accent px-2 py-1 text-[10px] font-medium text-white disabled:opacity-40"
                  >
                    {t("settings.tuning.save")}
                  </button>
                </label>
              ))}
            </div>
          </div>
        ))
      )}
    </Panel>
  );
}
'''
io.open(p, "w", encoding="utf-8", newline="\n").write(src)
print("settings ok")

# --- ExtensionsModal: PECL "get more" section ---------------------------------
p = FE + r"\src\pages\Projects.tsx"
src = io.open(p, encoding="utf-8").read()

src = src.replace(
    "  ListPhpExtensions,\n  SaveProjectVhost,",
    "  InstallPeclExtension,\n  ListPhpExtensions,\n  PeclExtensions,\n  SaveProjectVhost,",
    1,
)

old = '''  const refresh = useCallback(() => {
    ListPhpExtensions(series)
      .then((list) => setRows(list ?? []))
      .catch(() => setRows([]));
  }, [series]);'''
new = '''  const [pecl, setPecl] = useState<{ name: string; label: string; note?: string }[]>([]);
  const [installing, setInstalling] = useState("");

  const refresh = useCallback(() => {
    ListPhpExtensions(series)
      .then((list) => setRows(list ?? []))
      .catch(() => setRows([]));
    PeclExtensions()
      .then((list) => setPecl(list ?? []))
      .catch(() => setPecl([]));
  }, [series]);'''
assert old in src, "pecl state"
src = src.replace(old, new, 1)

old = '''          {visible.length === 0 && (
            <p className="px-3 py-6 text-center text-xs text-white/40">{t("projects.extensions.empty")}</p>
          )}'''
new = '''          {visible.length === 0 && (
            <p className="px-3 py-6 text-center text-xs text-white/40">{t("projects.extensions.empty")}</p>
          )}
          <div className="mt-3 border-t border-white/10 px-2 pt-2">
            <p className="mb-1 text-[10px] uppercase tracking-wide text-white/40">
              {t("projects.extensions.pecl")}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {pecl.map((ext) => (
                <button
                  key={ext.name}
                  type="button"
                  disabled={installing !== ""}
                  title={ext.note}
                  onClick={async () => {
                    setInstalling(ext.name);
                    try {
                      await InstallPeclExtension(series, ext.name);
                      refresh();
                    } catch (error) {
                      await dialogs.alert({ title: ext.label, message: String(error) });
                    } finally {
                      setInstalling("");
                    }
                  }}
                  className="flex items-center gap-1 rounded border border-white/10 bg-white/5 px-2 py-1 text-[10px] text-white/60 hover:bg-white/10 hover:text-white disabled:opacity-40"
                >
                  {installing === ext.name ? t("projects.extensions.installing") : "+ " + ext.label}
                </button>
              ))}
            </div>
          </div>'''
assert old in src, "pecl section"
src = src.replace(old, new, 1)
io.open(p, "w", encoding="utf-8", newline="\n").write(src)
print("extensions modal ok")

# --- i18n -----------------------------------------------------------------------
tuning_en = {
    "title": "Server tuning",
    "help": "Common nginx and PHP directives, edited with validation and applied with a reload. Empty means the default.",
    "empty": "Install nginx or PHP to tune them here.",
    "default": "default",
    "save": "Set",
}
tuning_id = {
    "title": "Tuning server",
    "help": "Direktif umum nginx dan PHP, divalidasi dan diterapkan dengan reload. Kosong berarti bawaan.",
    "empty": "Pasang nginx atau PHP untuk menyetelnya di sini.",
    "default": "bawaan",
    "save": "Setel",
}
ext_en = {"pecl": "Install from PECL (prebuilt DLL)", "installing": "installing…"}
ext_id = {"pecl": "Pasang dari PECL (DLL pra-bangun)", "installing": "memasang…"}

for rel, tuning, ext in (
    (r"\\langs\\en.json", tuning_en, ext_en),
    (r"\\langs\\id.json", tuning_id, ext_id),
):
    path = DK + rel
    with open(path, encoding="utf-8") as f:
        doc = json.load(f, object_pairs_hook=collections.OrderedDict)
    doc["settings"]["tuning"] = collections.OrderedDict(tuning)
    doc["projects"]["extensions"].update(ext)
    with open(path, "w", encoding="utf-8", newline="\\n") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
        f.write("\\n")
    print("i18n", rel)

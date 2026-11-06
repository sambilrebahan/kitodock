import { useT } from "../lib/i18n";

// Shared shell for sections that arrive in later phases. Each page gets its
// own module so it can grow in place without touching the shell.
export function Placeholder({ phase }: { phase: number }) {
  const t = useT();
  return (
    <div className="flex h-full items-center justify-center">
      <div className="glass px-8 py-6 text-center">
        <p className="text-sm text-white/60">{t("page.placeholder", { phase })}</p>
      </div>
    </div>
  );
}

export default Placeholder;

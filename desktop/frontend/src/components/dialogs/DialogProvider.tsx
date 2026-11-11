// Promise-based confirm / alert / prompt dialogs replacing the browser's
// native boxes, which render as an ugly "wails.localhost says" window and
// cannot be styled. One dialog shows at a time; the rest queue behind it.
//
//   if (!(await dialogs.confirm({ title: "Hapus cron ini?", danger: true }))) return;
//   const name = await dialogs.prompt({ title: "Nama baru", defaultValue: old });
//
// The queue lives in a provider so any subtree can reach it through
// useDialogs() without prop drilling.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { AlertTriangle, Info } from "lucide-react";
import { useT } from "../../lib/i18n";

type ConfirmRequest = {
  kind: "confirm";
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  resolve: (ok: boolean) => void;
};

type AlertRequest = {
  kind: "alert";
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  resolve: () => void;
};

type PromptRequest = {
  kind: "prompt";
  title: string;
  message?: ReactNode;
  label?: string;
  defaultValue?: string;
  placeholder?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  resolve: (value: string | null) => void;
};

type AnyRequest = ConfirmRequest | AlertRequest | PromptRequest;

export type Dialogs = {
  confirm(options: Omit<ConfirmRequest, "kind" | "resolve"> | string): Promise<boolean>;
  alert(options: Omit<AlertRequest, "kind" | "resolve"> | string): Promise<void>;
  prompt(options: Omit<PromptRequest, "kind" | "resolve"> | string): Promise<string | null>;
};

// Isolated component tests render without a provider; fall back to the
// browser's dialogs there so nothing silently does nothing.
const browserFallback: Dialogs = {
  confirm: (o) =>
    Promise.resolve(window.confirm(typeof o === "string" ? o : o.title)),
  alert: (o) => {
    window.alert(typeof o === "string" ? o : o.title);
    return Promise.resolve();
  },
  prompt: (o) =>
    Promise.resolve(
      typeof o === "string" ? window.prompt(o) : window.prompt(o.title, o.defaultValue),
    ),
};

const DialogsContext = createContext<Dialogs>(browserFallback);

export function useDialogs(): Dialogs {
  return useContext(DialogsContext);
}

let ticket = 0;

export function DialogProvider({ children }: { children: ReactNode }) {
  const [queue, setQueue] = useState<Array<AnyRequest & { id: number }>>([]);

  const push = useCallback((request: AnyRequest) => {
    ticket += 1;
    setQueue((waiting) => [...waiting, { ...request, id: ticket }]);
  }, []);

  const dialogs = useMemo<Dialogs>(
    () => ({
      confirm: (o) =>
        new Promise<boolean>((resolve) => {
          const base = typeof o === "string" ? { title: o } : o;
          push({ kind: "confirm", ...base, resolve });
        }),
      alert: (o) =>
        new Promise<void>((resolve) => {
          const base = typeof o === "string" ? { title: o } : o;
          push({ kind: "alert", ...base, resolve });
        }),
      prompt: (o) =>
        new Promise<string | null>((resolve) => {
          const base = typeof o === "string" ? { title: o } : o;
          push({ kind: "prompt", ...base, resolve });
        }),
    }),
    [push],
  );

  const active = queue[0] ?? null;
  const dismiss = useCallback((done: AnyRequest & { id: number }) => {
    setQueue((waiting) => waiting.filter((entry) => entry.id !== done.id));
  }, []);

  return (
    <DialogsContext.Provider value={dialogs}>
      {children}
      {active &&
        createPortal(<ActiveDialog key={active.id} request={active} onDismiss={dismiss} />, document.body)}
    </DialogsContext.Provider>
  );
}

type ActiveProps = {
  request: AnyRequest & { id: number };
  onDismiss: (done: AnyRequest & { id: number }) => void;
};

function ActiveDialog({ request, onDismiss }: ActiveProps) {
  const t = useT();
  const isPrompt = request.kind === "prompt";
  const isConfirm = request.kind === "confirm";
  const [draft, setDraft] = useState(isPrompt ? request.defaultValue ?? "" : "");
  const input = useRef<HTMLInputElement>(null);
  const accept = useRef<HTMLButtonElement>(null);
  const trimmed = draft.trim();
  const submittable = !isPrompt || trimmed.length > 0;

  const settleCancel = () => {
    if (isConfirm) request.resolve(false);
    else if (isPrompt) request.resolve(null);
    else request.resolve();
    onDismiss(request);
  };
  const settleAccept = () => {
    if (!submittable) return;
    if (isConfirm) request.resolve(true);
    else if (isPrompt) request.resolve(trimmed);
    else request.resolve();
    onDismiss(request);
  };

  useEffect(() => {
    if (isPrompt) {
      input.current?.focus();
      input.current?.select();
    } else {
      accept.current?.focus();
    }
  }, [isPrompt]);

  // Capture-phase keys: an open dialog must swallow Escape/Enter before any
  // modal underneath (which also listens on window) reacts to them.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopImmediatePropagation();
        settleCancel();
      } else if (event.key === "Enter" && !(event.target instanceof HTMLButtonElement)) {
        event.preventDefault();
        event.stopImmediatePropagation();
        settleAccept();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  });

  const danger = isConfirm && request.danger === true;
  const acceptLabel =
    request.confirmLabel ??
    (request.kind === "alert" ? t("dialog.ok") : isPrompt ? t("dialog.save") : t("dialog.confirm"));
  const declineLabel = request.kind === "alert" ? null : request.cancelLabel ?? t("dialog.cancel");
  const Icon = danger ? AlertTriangle : Info;

  return (
    <div
      data-dialog-backdrop
      onClick={settleCancel}
      className="fixed inset-0 z-[90] flex items-center justify-center bg-[#05030f]/70 backdrop-blur-sm"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="app-dialog-title"
        onClick={(event) => event.stopPropagation()}
        className="modal-card flex w-[440px] max-w-[92vw] flex-col gap-4 p-5"
      >
        <header className="flex items-start gap-3">
          <span
            className={`mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg ${
              danger ? "bg-red-500/15 text-red-300" : "bg-accent/15 text-accent2"
            }`}
          >
            <Icon className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1">
            <h2 id="app-dialog-title" className="text-sm font-semibold text-white/95">
              {request.title}
            </h2>
            {request.message && (
              <p className="mt-1 whitespace-pre-line break-words text-xs leading-relaxed text-white/65">
                {request.message}
              </p>
            )}
          </div>
        </header>

        {isPrompt && (
          <label className="flex flex-col gap-1.5 text-xs">
            {request.label && <span className="text-white/70">{request.label}</span>}
            <input
              ref={input}
              className="input w-full"
              value={draft}
              placeholder={request.placeholder}
              onChange={(event) => setDraft(event.target.value)}
              spellCheck={false}
              autoComplete="off"
            />
          </label>
        )}

        <footer className="flex justify-end gap-2">
          {declineLabel && (
            <button
              type="button"
              onClick={settleCancel}
              className="rounded-md border border-white/10 px-3 py-1.5 text-xs text-white/75 hover:bg-white/10 hover:text-white"
            >
              {declineLabel}
            </button>
          )}
          <button
            ref={accept}
            type="button"
            onClick={settleAccept}
            disabled={!submittable}
            data-danger={danger ? "true" : undefined}
            className={`rounded-md px-3 py-1.5 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-40 ${
              danger
                ? "bg-red-500/85 text-white hover:bg-red-500"
                : "bg-accent text-white hover:bg-accent/85"
            }`}
          >
            {acceptLabel}
          </button>
        </footer>
      </div>
    </div>
  );
}

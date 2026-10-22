"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { Button, cx, inputClass } from "./ui";

type ConfirmOptions = {
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  tone?: "danger" | "primary";
  /** Requires typing this text to enable confirmation (irreversible actions). */
  requireText?: string;
};

type Ask = (opts: ConfirmOptions) => Promise<boolean>;
const Confirm = createContext<Ask>(async () => false);

/** `const confirm = useConfirm(); if (await confirm({ title: "…" })) …` */
export const useConfirm = () => useContext(Confirm);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOptions | null>(null);
  const resolver = useRef<((ok: boolean) => void) | null>(null);

  const ask = useCallback<Ask>((o) => {
    resolver.current?.(false);
    setOpts(o);
    return new Promise((resolve) => (resolver.current = resolve));
  }, []);

  const close = useCallback((ok: boolean) => {
    resolver.current?.(ok);
    resolver.current = null;
    setOpts(null);
  }, []);

  return (
    <Confirm.Provider value={ask}>
      {children}
      {opts && <ConfirmDialog opts={opts} onClose={close} />}
    </Confirm.Provider>
  );
}

function ConfirmDialog({ opts, onClose }: { opts: ConfirmOptions; onClose: (ok: boolean) => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const [typed, setTyped] = useState("");
  const tone = opts.tone ?? "danger";
  const blocked = !!opts.requireText && typed !== opts.requireText;

  useEffect(() => {
    ref.current?.showModal();
  }, []);

  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        onClose(false);
      }}
      onClick={(e) => e.target === ref.current && onClose(false)}
      className="m-auto w-[min(26rem,calc(100vw-2rem))] rounded-xl border border-border bg-surface p-0 text-fg shadow-2xl backdrop:bg-black/50 backdrop:backdrop-blur-[2px] open:animate-[confirm-in_120ms_ease-out]"
    >
      <form
        method="dialog"
        onSubmit={(e) => {
          e.preventDefault();
          if (!blocked) onClose(true);
        }}
        className="flex flex-col gap-4 p-5"
      >
        <div className="flex gap-3">
          <span
            className={cx(
              "grid size-9 shrink-0 place-items-center rounded-full",
              tone === "danger" ? "bg-danger/10 text-danger" : "bg-accent/10 text-accent",
            )}
            aria-hidden
          >
            <svg viewBox="0 0 24 24" className="size-5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" />
            </svg>
          </span>
          <div className="min-w-0">
            <h2 className="text-base font-semibold">{opts.title}</h2>
            {opts.message && <div className="mt-1 text-sm text-muted">{opts.message}</div>}
          </div>
        </div>

        {opts.requireText && (
          <label className="flex flex-col gap-1.5 text-xs text-muted">
            <span>
              Type <code className="rounded bg-surface-2 px-1 py-0.5 font-mono text-fg">{opts.requireText}</code> to confirm
            </span>
            <input value={typed} onChange={(e) => setTyped(e.target.value)} className={inputClass} autoComplete="off" spellCheck={false} />
          </label>
        )}

        <div className="flex justify-end gap-2">
          <Button type="button" autoFocus={!opts.requireText} onClick={() => onClose(false)}>
            Cancel
          </Button>
          <Button type="submit" disabled={blocked} variant={tone === "danger" ? "danger-solid" : "primary"}>
            {opts.confirmLabel ?? "Confirm"}
          </Button>
        </div>
      </form>
    </dialog>
  );
}

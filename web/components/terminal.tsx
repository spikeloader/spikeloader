"use client";

import { useEffect, useRef, useState } from "react";
import { useLab } from "./providers";
import { Button, StatusBadge } from "./ui";

/** Interactive terminal over WebSocket (/ws/terminal). target = "outer" or the microVM id. */
export function Terminal({ target, height = 460 }: { target: string; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<"connecting" | "open" | "closed">("connecting");
  const [session, setSession] = useState(0);
  const host = useLab().status?.host?.hostname ?? "outer-vm";

  useEffect(() => {
    let disposed = false;
    let cleanup = () => {};

    (async () => {
      const [{ Terminal: XTerm }, { FitAddon }] = await Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]);
      await document.fonts.ready;
      if (disposed || !el.current) return;

      const term = new XTerm({
        cursorBlink: true,
        // xterm draws on a canvas: it needs the real font name, not var(--...)
        fontFamily: `${getComputedStyle(document.documentElement).getPropertyValue("--font-geist-mono").trim() || "ui-monospace"}, monospace`,
        fontSize: 13,
        theme: { background: "#00000000", foreground: "#e4e4e7", cursor: "#ff7a2f", selectionBackground: "#ff7a2f55" },
        allowTransparency: true,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(el.current);
      fit.fit();

      const proto = location.protocol === "https:" ? "wss" : "ws";
      const ws = new WebSocket(`${proto}://${location.host}/ws/terminal?target=${target}&cols=${term.cols}&rows=${term.rows}`);
      setState("connecting");
      ws.onopen = () => {
        setState("open");
        term.focus();
      };
      ws.onmessage = (e) => term.write(typeof e.data === "string" ? e.data : "");
      ws.onclose = () => setState("closed");

      const input = term.onData((d) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify({ t: "i", d })));
      const resize = term.onResize(({ cols, rows }) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify({ t: "r", c: cols, r: rows })));
      const ro = new ResizeObserver(() => fit.fit());
      ro.observe(el.current);

      cleanup = () => {
        ro.disconnect();
        input.dispose();
        resize.dispose();
        ws.close();
        term.dispose();
      };
    })();

    return () => {
      disposed = true;
      cleanup();
    };
  }, [target, session]);

  return (
    <div className="overflow-hidden rounded-lg border border-border bg-term">
      <div className="flex items-center gap-2 border-b border-white/10 px-3 py-1.5 text-xs text-neutral-400">
        <StatusBadge state={state === "open" ? "running" : state === "connecting" ? "paused" : "stopped"} label={state === "open" ? "connected" : state === "connecting" ? "connecting…" : "disconnected"} />
        <span className="font-mono">{target === "outer" ? `lab@${host}` : `root@vm${target}`}</span>
        <Button size="sm" variant="ghost" className="ml-auto !text-neutral-400 hover:!bg-white/10" onClick={() => setSession((s) => s + 1)}>
          reconnect
        </Button>
      </div>
      <div ref={el} className="p-2" style={{ height }} />
    </div>
  );
}

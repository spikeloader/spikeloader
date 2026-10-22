"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { use, useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import type { MicroVM } from "@/lib/types";
import { api, useLab } from "./providers";
import { Terminal } from "./terminal";
import { Button, Card, Empty, KV, MetaItem, Meter, PageHeader, StatusBadge, Tabs, cx, fmtBytes, fmtDuration, inputClass } from "./ui";

const TABS = [
  { id: "terminal", label: "Terminal", icon: "terminal" },
  { id: "console", label: "Serial console", icon: "list" },
  { id: "exec", label: "Run command", icon: "play" },
  { id: "api", label: "Firecracker API", icon: "server" },
] as const;
type Tab = (typeof TABS)[number]["id"];

export function VmDetail({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const { status } = useLab();
  const search = useSearchParams();
  const router = useRouter();
  const path = usePathname();
  const tab = (TABS.find((t) => t.id === search.get("tab"))?.id ?? "terminal") as Tab;
  const vm = status?.vms.find((v) => String(v.id) === id);

  if (!status) return <div className="text-sm text-muted">loading…</div>;
  if (!vm)
    return (
      <Empty icon="box" title={`vm${id} no longer exists`} action={<Link href="/infra" className="text-sm text-accent">Back to infrastructure</Link>}>
        CI microVMs are destroyed as soon as their job ends — the job log keeps everything that happened.
      </Empty>
    );

  const live = vm.state !== "stopped";
  const run = Number(vm.label?.match(/run #(\d+)/)?.[1]) || null;
  return (
    <>
      <PageHeader
        back={{ href: "/infra", label: "Infrastructure" }}
        title={<span className="font-mono">{vm.name}</span>}
        badge={<StatusBadge state={vm.state} />}
        description={
          run ? (
            <>
              Running <Link href={`/ci/runs/${run}`} className="text-accent hover:underline">{vm.label}</Link>. It is destroyed when the job ends.
            </>
          ) : (
            (vm.label ?? "MicroVM")
          )
        }
        meta={
          <>
            <MetaItem icon="link">{vm.ip}</MetaItem>
            <MetaItem icon="cpu">{vm.vcpus} vCPU</MetaItem>
            <MetaItem icon="memory">{vm.mem_mib} MiB</MetaItem>
            <MetaItem icon="clock">up {fmtDuration(vm.uptime_s)}</MetaItem>
          </>
        }
      />

      <div className="grid gap-6 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <Card title="Details" icon="info">
          <KV
            rows={[
              ["IP", vm.ip],
              ["Gateway (TAP)", `${vm.tap_ip} · ${vm.tap}`],
              ["MAC", vm.mac],
              ["Firecracker PID", vm.pid ? String(vm.pid) : "—"],
              ["Uptime", fmtDuration(vm.uptime_s)],
            ]}
          />
          <div className="mt-4 flex flex-col gap-3">
            <Meter label="Memory" icon="memory" value={vm.rss_kb / 1024} max={vm.mem_mib} detail={`${fmtBytes(vm.rss_kb * 1024)} / ${vm.mem_mib} MiB`} />
            <Meter label="CPU (process)" icon="cpu" value={vm.cpu_pct} max={vm.vcpus * 100} detail={`${vm.cpu_pct.toFixed(1)}%`} />
            <Meter label="Disk (allocated)" icon="disk" value={vm.disk_used_bytes} max={vm.disk_size_bytes} detail={`${fmtBytes(vm.disk_used_bytes)} / ${fmtBytes(vm.disk_size_bytes)}`} />
          </div>
          <div className="mt-4 border-t border-border pt-3 text-[11px] break-all text-muted">
            <span className="font-medium">boot args</span> <span className="font-mono">{vm.boot_args}</span>
          </div>
        </Card>

        <div className="flex min-w-0 flex-col gap-4">
          <Tabs value={tab} onChange={(t) => router.replace(`${path}?tab=${t}`, { scroll: false })} tabs={TABS.map((t) => ({ ...t }))} />
          {tab === "terminal" && (vm.state === "running" ? <Terminal key={vm.id} target={String(vm.id)} /> : <Empty icon="terminal" title="Not running">A terminal needs the microVM to be running.</Empty>)}
          {tab === "console" && <SerialConsole id={vm.id} />}
          {tab === "exec" && (live ? <Exec vm={vm} /> : <Empty icon="terminal" title="Not running">Commands need the microVM to be running.</Empty>)}
          {tab === "api" && (live ? <FcApi id={vm.id} /> : <Empty icon="server" title="Not running">The Firecracker API only exists while the microVM is on.</Empty>)}
        </div>
      </div>
    </>
  );
}

function SerialConsole({ id }: { id: number }) {
  const [text, setText] = useState("");
  const [follow, setFollow] = useState(true);
  const ref = useRef<HTMLPreElement>(null);

  const load = useCallback(async () => {
    try {
      setText((await api<{ text: string }>(`/api/vms/${id}/console?lines=500`)).text);
    } catch (e) {
      setText(`error: ${(e as Error).message}`);
    }
  }, [id]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial polling load
    load();
    if (!follow) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [load, follow]);

  useEffect(() => {
    if (follow && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [text, follow]);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-3 text-xs text-muted">
        <label className="flex items-center gap-1.5">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} className="accent-[var(--accent)]" />
          follow (last 500 lines)
        </label>
        <Button size="sm" variant="ghost" onClick={load}>
          refresh
        </Button>
      </div>
      <pre ref={ref} className="h-[460px] overflow-auto rounded-lg border border-border bg-term p-3 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-neutral-300">
        {text || "loading…"}
      </pre>
    </div>
  );
}

type ExecResult = { code: number; stdout: string; stderr: string; durationMs: number };
const SNIPPETS = ["uname -a", "id && hostname", "free -m", "df -h /", "ip -br addr && ip route", "ps aux --sort=-%mem | head", "curl -sI https://github.com | head -1"];

function Exec({ vm }: { vm: MicroVM }) {
  const [cmd, setCmd] = useState("uname -a");
  const [history, setHistory] = useState<(ExecResult & { cmd: string })[]>([]);
  const [busy, setBusy] = useState(false);

  const run = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!cmd.trim()) return;
    setBusy(true);
    try {
      const r = await api<ExecResult>(`/api/vms/${vm.id}/exec`, { method: "POST", body: { command: cmd } });
      setHistory((h) => [{ ...r, cmd }, ...h].slice(0, 20));
    } catch (err) {
      setHistory((h) => [{ cmd, code: -1, stdout: "", stderr: (err as Error).message, durationMs: 0 }, ...h]);
    }
    setBusy(false);
  };

  return (
    <div className="flex flex-col gap-3">
      <form onSubmit={run} className="flex gap-2">
        <span className="grid place-items-center font-mono text-sm text-accent">#</span>
        <input value={cmd} onChange={(e) => setCmd(e.target.value)} className={cx(inputClass, "flex-1")} placeholder="command run as root" />
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? "running…" : "Run"}
        </Button>
      </form>
      <div className="flex flex-wrap gap-1">
        {SNIPPETS.map((s) => (
          <button key={s} onClick={() => setCmd(s)} className="rounded border border-border px-2 py-0.5 font-mono text-[11px] text-muted hover:text-fg">
            {s}
          </button>
        ))}
      </div>
      {history.map((h, i) => (
        <div key={history.length - i} className="overflow-hidden rounded-lg border border-border">
          <div className="flex items-center gap-2 bg-surface-2 px-3 py-1.5 font-mono text-xs">
            <span className="text-accent">root@{vm.name}#</span>
            <span className="min-w-0 flex-1 truncate">{h.cmd}</span>
            <span className={h.code === 0 ? "text-ok" : "text-danger"}>exit {h.code}</span>
            <span className="text-muted">{h.durationMs} ms</span>
          </div>
          <pre className="max-h-80 overflow-auto bg-term p-3 font-mono text-[11px] whitespace-pre-wrap text-neutral-300">
            {h.stdout}
            {h.stderr && <span className="text-red-400">{h.stderr}</span>}
            {!h.stdout && !h.stderr && <span className="text-neutral-500">(no output)</span>}
          </pre>
        </div>
      ))}
    </div>
  );
}

const API_PATHS = ["/", "/vm/config", "/machine-config", "/version", "/balloon", "/mmds"];

function FcApi({ id }: { id: number }) {
  const [path, setPath] = useState("/vm/config");
  const [res, setRes] = useState<{ ok: boolean; data: unknown; error: string | null } | null>(null);

  useEffect(() => {
    let cancel = false;
    api<{ ok: boolean; data: unknown; error: string | null }>(`/api/vms/${id}/api?path=${encodeURIComponent(path)}`)
      .then((r) => !cancel && setRes(r))
      .catch((e) => !cancel && setRes({ ok: false, data: null, error: (e as Error).message }));
    return () => {
      cancel = true;
    };
  }, [id, path]);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-1">
        <span className="mr-1 font-mono text-xs text-muted">GET</span>
        {API_PATHS.map((p) => (
          <button key={p} onClick={() => setPath(p)} className={cx("rounded border px-2 py-0.5 font-mono text-[11px]", p === path ? "border-accent text-accent" : "border-border text-muted hover:text-fg")}>
            {p}
          </button>
        ))}
      </div>
      <p className="text-xs text-muted">Direct query to this microVM&apos;s Firecracker API socket (read-only).</p>
      <pre className="h-[420px] overflow-auto rounded-lg border border-border bg-term p-3 font-mono text-[11px] text-neutral-300">
        {!res ? "loading…" : res.ok ? JSON.stringify(res.data, null, 2) : <span className="text-red-400">{res.error}</span>}
      </pre>
    </div>
  );
}

"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { use, useEffect, useState, type FormEvent } from "react";
import type { ProjectView, RunSummary, SecretInfo } from "@/lib/ci/types";
import { useConfirm } from "../confirm";
import { api, useJobs } from "../providers";
import { Button, Card, CopyField, Empty, Field, MetaItem, Notice, PageHeader, Stat, Tabs, fmtDuration, inputClass } from "../ui";
import { CiStatus, RunsList, SecretsEditor, ago, every, shortRepo, usePoll } from "./shared";

type Detail = { project: ProjectView; webhookToken: string; secrets: SecretInfo[]; runs: RunSummary[] };
type Tab = "runs" | "branches" | "settings" | "secrets" | "webhook";

export function CiProject({ params }: { params: Promise<{ project: string }> }) {
  const { project: id } = use(params);
  const router = useRouter();
  const path = usePathname();
  const search = useSearchParams();
  const { notify } = useJobs();
  const { data, error, reload } = usePoll<Detail>(`/api/ci/projects/${id}`, 3000);
  const [ref, setRef] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const tab = (["runs", "branches", "settings", "secrets", "webhook"].includes(search.get("tab") ?? "") ? search.get("tab") : "runs") as Tab;

  if (error && !data) return <Empty icon="alert" title={error} action={<Link href="/ci" className="text-sm text-accent">Back to pipelines</Link>} />;
  if (!data) return <div className="text-sm text-muted">loading…</div>;
  const p = data.project;
  const runs = data.runs;
  const finished = runs.filter((r) => r.status === "success" || r.status === "failed");
  const rate = finished.length ? Math.round((finished.filter((r) => r.status === "success").length / finished.length) * 100) : null;
  const times = finished.filter((r) => r.startedAt && r.endedAt).map((r) => (r.endedAt! - r.startedAt!) / 1000);
  const avg = times.length ? Math.round(times.reduce((a, b) => a + b, 0) / times.length) : null;

  const run = async (e: FormEvent) => {
    e.preventDefault();
    setBusy("run");
    try {
      const r = await api<RunSummary>(`/api/ci/projects/${id}/run`, { method: "POST", body: { ref } });
      router.push(`/ci/runs/${r.id}`);
    } catch (err) {
      notify((err as Error).message);
      setBusy(null);
    }
  };

  const sync = async () => {
    setBusy("sync");
    await api(`/api/ci/projects/${id}/sync`, { method: "POST" }).catch((e) => notify((e as Error).message));
    await reload();
    setBusy(null);
  };

  return (
    <>
      <PageHeader
        back={{ href: "/ci", label: "Pipelines" }}
        title={p.name}
        badge={p.lastRun && <CiStatus status={p.lastRun.status} />}
        meta={
          <>
            <MetaItem icon="link" title={p.url}>
              <span className="font-mono">{shortRepo(p.url)}</span>
            </MetaItem>
            <MetaItem icon="branch">
              default <span className="font-mono">{p.defaultBranch}</span>
            </MetaItem>
            <MetaItem icon="refresh">{p.pollS ? `checks every ${every(p.pollS)}` : "webhook only"}</MetaItem>
            <MetaItem icon="clock">fetched {ago(p.lastSyncAt)}</MetaItem>
          </>
        }
        actions={
          <form onSubmit={run} className="flex items-center gap-2">
            <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder={p.defaultBranch} className={`${inputClass} w-36 sm:w-44`} title="branch, tag or commit" aria-label="branch, tag or commit" />
            <Button type="submit" variant="primary" icon="play" disabled={!!busy}>
              {busy === "run" ? "Starting…" : "Run"}
            </Button>
            <Button type="button" icon="refresh" onClick={sync} disabled={!!busy} title="Fetch new commits now">
              <span className="hidden sm:inline">{busy === "sync" ? "Fetching…" : "Fetch"}</span>
            </Button>
          </form>
        }
      />

      {p.lastError && (
        <Notice tone="danger">
          <span className="font-medium">Cannot fetch the repository.</span> <span className="font-mono text-xs">{p.lastError}</span>
        </Notice>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="Last run" icon="activity" value={p.lastRun ? `#${p.lastRun.id}` : "—"} hint={p.lastRun ? `${p.lastRun.status} · ${ago(p.lastRun.createdAt)}` : "no runs yet"} />
        <Stat label="Success rate" icon="check" value={rate === null ? "—" : `${rate}%`} tone={rate === null ? undefined : rate >= 80 ? "ok" : rate >= 50 ? "warn" : "danger"} hint={`${finished.length} finished runs`} />
        <Stat label="Average duration" icon="clock" value={avg === null ? "—" : fmtDuration(avg)} />
        <Stat label="Branches" icon="branch" value={Object.keys(p.refs).length} hint={`${data.secrets.length} secret${data.secrets.length === 1 ? "" : "s"}`} />
      </div>

      <div className="flex flex-col gap-4">
        <Tabs
          value={tab}
          onChange={(t) => router.replace(`${path}${t === "runs" ? "" : `?tab=${t}`}`, { scroll: false })}
          tabs={[
            { id: "runs", label: "Runs", icon: "list", count: runs.length },
            { id: "branches", label: "Branches", icon: "branch", count: Object.keys(p.refs).length },
            { id: "settings", label: "Settings", icon: "sliders" },
            { id: "secrets", label: "Secrets", icon: "key", count: data.secrets.length },
            { id: "webhook", label: "Webhook", icon: "link" },
          ]}
        />

        {tab === "runs" && (
          <Card flush>
            <RunsList runs={runs} empty={<Empty icon="list" title="No runs yet">Push a commit with a <code className="font-mono">.spikeloader.yml</code> or press Run.</Empty>} />
          </Card>
        )}
        {tab === "branches" && <Branches p={p} runs={runs} />}
        {tab === "settings" && <Settings p={p} onSaved={reload} />}
        {tab === "secrets" && (
          <Card title="Project secrets" icon="key" description="Override global secrets with the same name." className="max-w-3xl">
            <SecretsEditor scope={id} names={data.secrets} onChange={reload} />
          </Card>
        )}
        {tab === "webhook" && <Webhook id={id} token={data.webhookToken} onRotated={reload} />}
      </div>
    </>
  );
}

function Branches({ p, runs }: { p: ProjectView; runs: RunSummary[] }) {
  const entries = Object.entries(p.refs).sort(([a], [b]) => (a === p.defaultBranch ? -1 : b === p.defaultBranch ? 1 : a.localeCompare(b)));
  if (!entries.length) return <Empty icon="branch" title="Not fetched yet">Press Fetch to read the branches of the repository.</Empty>;
  return (
    <Card flush>
      <ul className="divide-y divide-border">
        {entries.map(([b, sha]) => {
          const last = runs.find((r) => r.branch === b);
          return (
            <li key={b} className="flex items-center gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate font-mono text-sm">{b}</span>
                  {b === p.defaultBranch && <span className="rounded bg-surface-2 px-1.5 py-0.5 text-[11px] text-muted">default</span>}
                </div>
                <div className="font-mono text-xs text-muted">{sha.slice(0, 10)}</div>
              </div>
              {last ? (
                <Link href={`/ci/runs/${last.id}`} className="flex items-center gap-2 text-xs text-muted hover:text-fg">
                  #{last.id} <CiStatus status={last.status} />
                </Link>
              ) : (
                <span className="text-xs text-muted">no runs</span>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

function Settings({ p, onSaved }: { p: ProjectView; onSaved: () => void }) {
  const { notify } = useJobs();
  const confirm = useConfirm();
  const router = useRouter();
  const [form, setForm] = useState({ url: "", defaultBranch: p.defaultBranch, protectedBranches: p.protectedBranches.join(", "), pollS: p.pollS, notifyUrl: p.notifyUrl, notifyOn: p.notifyOn });
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (!saved) return;
    const t = setTimeout(() => setSaved(false), 2000);
    return () => clearTimeout(t);
  }, [saved]);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await api(`/api/ci/projects/${p.id}`, { method: "PATCH", body: { ...form, protectedBranches: form.protectedBranches.split(",").map((p) => p.trim()).filter(Boolean) } });
      setForm((f) => ({ ...f, url: "" }));
      setSaved(true);
      onSaved();
    } catch (err) {
      notify((err as Error).message);
    }
  };

  const remove = async () => {
    const ok = await confirm({
      title: `Delete ${p.name}?`,
      message: "Runs, logs, artifacts, caches, secrets and the git mirror of this project are deleted.",
      confirmLabel: "Delete project",
      requireText: p.id,
    });
    if (!ok) return;
    await api(`/api/ci/projects/${p.id}`, { method: "DELETE" });
    router.push("/ci");
  };

  return (
    <div className="grid max-w-3xl gap-6">
      <form onSubmit={save}>
        <Card
          title="Repository & notifications"
          icon="sliders"
          actions={
            <Button type="submit" size="sm" variant="primary" icon={saved ? "check" : undefined}>
              {saved ? "Saved" : "Save changes"}
            </Button>
          }
        >
          <div className="flex flex-col gap-4">
            <Field label="Git URL" hint={`Currently ${p.url}. Leave empty to keep it.`}>
              <input value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} placeholder="new URL" className={inputClass} spellCheck={false} />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Default branch" hint="Used by manual runs and schedules.">
                <input value={form.defaultBranch} onChange={(e) => setForm({ ...form, defaultBranch: e.target.value })} className={inputClass} />
              </Field>
              <Field label="Check for commits every (s)" hint="0 = only webhooks and manual runs.">
                <input type="number" min={0} value={form.pollS} onChange={(e) => setForm({ ...form, pollS: Number(e.target.value) })} className={inputClass} />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-[1fr_12rem]">
              <Field label="Protected branches" hint="Comma-separated branch globs, e.g. main, release/*. Empty denies protected secrets on all branches. Tags and detached commits never receive them.">
                <input value={form.protectedBranches} onChange={(e) => setForm({ ...form, protectedBranches: e.target.value })} className={inputClass} />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-[1fr_12rem]">
              <Field label="Notification webhook" hint="Slack, Discord or any URL that accepts a JSON POST.">
                <input value={form.notifyUrl} onChange={(e) => setForm({ ...form, notifyUrl: e.target.value })} placeholder="https://hooks.slack.com/…" className={inputClass} spellCheck={false} />
              </Field>
              <Field label="Notify on">
                <select value={form.notifyOn} onChange={(e) => setForm({ ...form, notifyOn: e.target.value as ProjectView["notifyOn"] })} className={inputClass}>
                  <option value="failure">failures</option>
                  <option value="always">every finished run</option>
                  <option value="never">never</option>
                </select>
              </Field>
            </div>
          </div>
        </Card>
      </form>
      <Card title="Danger zone" icon="alert">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm text-muted">Deletes the project with its runs, logs, artifacts, caches and secrets.</p>
          <Button variant="danger" icon="trash" onClick={remove}>
            Delete project
          </Button>
        </div>
      </Card>
    </div>
  );
}

function Webhook({ id, token, onRotated }: { id: string; token: string; onRotated: () => void }) {
  const confirm = useConfirm();
  const [origin, setOrigin] = useState("http://127.0.0.1:4100");
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- the origin is only known in the browser
    setOrigin(location.origin);
  }, []);
  const url = `${origin}/api/ci/hooks/${id}?token=${token}`;

  const rotate = async () => {
    if (!(await confirm({ title: "Rotate the webhook token?", message: "Webhooks configured with the current URL stop working.", confirmLabel: "Rotate" }))) return;
    await api(`/api/ci/projects/${id}`, { method: "PATCH", body: { rotateToken: true } });
    onRotated();
  };

  return (
    <Card
      title="Push webhook"
      icon="link"
      className="max-w-3xl"
      description="Starts runs right after a push instead of waiting for the next check."
      actions={
        <Button size="sm" icon="refresh" onClick={rotate}>
          Rotate token
        </Button>
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        <CopyField value={url} secret />
        <ol className="flex list-decimal flex-col gap-1.5 pl-5 text-muted">
          <li>In GitHub, GitLab, Gitea or Bitbucket, add a push webhook (POST, any content type) pointing to this URL.</li>
          <li>The panel only listens on localhost: expose just this path through a tunnel (e.g. cloudflared). The token is the only credential it accepts.</li>
          <li>No tunnel? Polling already picks up new commits.</li>
        </ol>
      </div>
    </Card>
  );
}

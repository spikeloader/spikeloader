"use client";

import type { CiImage, TemplateInfo } from "@/lib/ci/types";
import { useConfirm } from "../confirm";
import { Icon } from "../icons";
import { api, useJobs } from "../providers";
import { Button, Card, Empty, Notice, fmtBytes } from "../ui";
import { CiSection, ago, usePoll } from "./shared";

type Images = { built: CiImage[] | null; definitions: { name: string; description: string; from: string | null; size: string; key: string | null }[] };
type Overview = { templates: TemplateInfo[] };

export function CiLibrary() {
  const { data: images, error, reload } = usePoll<Images>("/api/ci/images", 10_000);
  const { data: overview } = usePoll<Overview>("/api/ci", 60_000);
  const confirm = useConfirm();
  const { notify } = useJobs();

  const remove = async (name: string) => {
    if (!(await confirm({ title: `Delete image ${name}?`, message: "The next job that needs it rebuilds it from its script.", confirmLabel: "Delete" }))) return;
    await api("/api/ci/images", { method: "DELETE", body: { name } }).catch((e) => notify((e as Error).message));
    reload();
  };

  const defs = images?.definitions ?? [];
  const offline = images?.built === null;
  const current = (name: string) => images?.built?.filter((b) => b.name === defs.find((d) => d.name === name)?.key) ?? [];
  const others = images?.built?.filter((b) => !defs.some((d) => d.key === b.name)) ?? [];

  return (
    <CiSection active="/ci/library">
      <Card
        title="Images"
        icon="box"
        description={
          <>
            Root filesystems the jobs boot from (<code className="font-mono">image:</code>). Built once from a shell script on first use; rebuilt when the script changes.
          </>
        }
        flush
      >
        {error && <p className="p-4 text-sm text-danger">{error}</p>}
        {offline && (
          <div className="border-b border-border p-4">
            <Notice>The outer VM is off, so built images cannot be listed. They are kept on its disk and reused when the next job starts it.</Notice>
          </div>
        )}
        <ul className="divide-y divide-border">
          {defs.map((d) => {
            const built = current(d.name);
            return (
              <li key={d.name} className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-sm font-semibold">{d.name}</span>
                    <span className="text-xs text-muted">
                      from {d.from ?? "Arch Linux"} · {d.size}
                    </span>
                  </div>
                  <div className="mt-0.5 text-xs text-muted">{d.description}</div>
                </div>
                <div className="flex flex-col gap-1 sm:items-end">
                  {built.length ? (
                    built.map((b) => (
                      <div key={b.name} className="flex items-center gap-2 text-xs">
                        <span className="inline-flex items-center gap-1 text-ok">
                          <Icon name="check" className="size-3.5" />
                          built
                        </span>
                        <span className="font-mono text-muted">
                          {fmtBytes(b.used_bytes)} · {ago(b.built_at * 1000)}
                        </span>
                        <Button size="sm" variant="ghost" icon="trash" onClick={() => remove(b.name)} title={`delete ${b.name}`} />
                      </div>
                    ))
                  ) : (
                    <span className="text-xs text-muted">{!images ? "…" : offline ? "outer VM off" : "built on first use"}</span>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
        {others.length > 0 && (
          <div className="border-t border-border px-4 py-3">
            <div className="mb-2 text-xs font-medium text-muted">Repository images and outdated versions (safe to delete)</div>
            <ul className="flex flex-col gap-1.5">
              {others.map((b) => (
                <li key={b.name} className="flex items-center gap-2 text-xs">
                  <span className="min-w-0 flex-1 truncate font-mono">{b.name}</span>
                  <span className="font-mono text-muted">
                    {fmtBytes(b.used_bytes)} · {ago(b.built_at * 1000)}
                  </span>
                  <Button size="sm" variant="ghost" icon="trash" onClick={() => remove(b.name)} title={`delete ${b.name}`} />
                </li>
              ))}
            </ul>
          </div>
        )}
        <p className="border-t border-border px-4 py-3 text-xs text-muted">
          Add or override images per repository in <code className="font-mono">.spikeloader/images/NAME.sh</code> — first lines <code className="font-mono"># from: base</code> and{" "}
          <code className="font-mono"># size: 8G</code>.
        </p>
      </Card>

      <Card title="Templates" icon="pipelines" description={<>Reusable jobs (<code className="font-mono">uses:</code>); the job&apos;s own keys override them and <code className="font-mono">with:</code> sets the inputs.</>}>
        {!overview ? (
          <p className="text-sm text-muted">loading…</p>
        ) : !overview.templates.length ? (
          <Empty icon="pipelines" title="No templates" />
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {overview.templates.map((t) => (
              <div key={t.name} className="flex flex-col gap-2 rounded-lg border border-border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-semibold">{t.name}</span>
                  <span className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-muted">{t.image}</span>
                </div>
                <p className="text-xs text-muted">{t.description}</p>
                <pre className="overflow-x-auto rounded-md bg-bg p-2 font-mono text-[11px] leading-relaxed">
                  {`uses: ${t.name}`}
                  {Object.keys(t.inputs).length > 0 && `\nwith: { ${Object.entries(t.inputs).map(([k, v]) => `${k}: ${JSON.stringify(v)}`).join(", ")} }`}
                  {t.secrets.length > 0 && `\nsecrets: [${t.secrets.join(", ")}]`}
                </pre>
              </div>
            ))}
          </div>
        )}
        <p className="mt-3 text-xs text-muted">
          Write your own in <code className="font-mono">.spikeloader/templates/NAME.yml</code> (subfolders allowed) — same keys as a job plus <code className="font-mono">inputs:</code>.
        </p>
      </Card>
    </CiSection>
  );
}

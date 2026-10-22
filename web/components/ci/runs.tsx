"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import type { RunSummary } from "@/lib/ci/types";
import { Card, Empty, cx, inputClass } from "../ui";
import type { Overview } from "./overview";
import { CiSection, RunsList, usePoll } from "./shared";

const STATUSES = ["all", "running", "waiting", "queued", "success", "failed", "canceled"] as const;

export function CiRuns() {
  const search = useSearchParams();
  const router = useRouter();
  const path = usePathname();
  const project = search.get("project") ?? "";
  const status = search.get("status") ?? "all";
  const { data: runs } = usePoll<RunSummary[]>(`/api/ci/runs?limit=200${project ? `&project=${encodeURIComponent(project)}` : ""}`, 3000);
  const { data: overview } = usePoll<Overview>("/api/ci", 30_000);

  const set = (k: string, v: string) => {
    const q = new URLSearchParams(search);
    if (v && v !== "all") q.set(k, v);
    else q.delete(k);
    router.replace(`${path}${q.size ? `?${q}` : ""}`, { scroll: false });
  };
  const shown = (runs ?? []).filter((r) => status === "all" || r.status === status);

  return (
    <CiSection active="/ci/runs">
      <Card
        title={runs ? `${shown.length} run${shown.length === 1 ? "" : "s"}` : "Runs"}
        icon="list"
        flush
        actions={
          <>
            <select value={project} onChange={(e) => set("project", e.target.value)} className={cx(inputClass, "h-8 w-auto font-sans text-xs")}>
              <option value="">All projects</option>
              {overview?.projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
            <div className="flex flex-wrap gap-1">
              {STATUSES.map((s) => (
                <button
                  key={s}
                  onClick={() => set("status", s)}
                  className={cx("rounded-full border px-2.5 py-1 text-xs capitalize transition", s === status ? "border-accent bg-accent/10 text-accent" : "border-border text-muted hover:text-fg")}
                >
                  {s}
                </button>
              ))}
            </div>
          </>
        }
      >
        {!runs ? <p className="p-4 text-sm text-muted">loading…</p> : <RunsList runs={shown} showProject={!project} empty={<Empty icon="list" title="No runs match these filters" />} />}
      </Card>
    </CiSection>
  );
}

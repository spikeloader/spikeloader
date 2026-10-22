"use client";

import { useState } from "react";
import { api } from "../providers";
import { Button, Card, Empty, Notice, cx } from "../ui";
import { CiSection } from "./shared";

const EXAMPLE = `stages: [build, test, scan, deploy]

jobs:
  build:
    stage: build
    image: node
    cache: { key: npm, files: [package-lock.json], paths: [node_modules] }
    steps:
      - npm ci
      - npm run build
    artifacts: [dist/]

  test:
    stage: test
    image: node
    matrix: { shard: ["1", "2"] }
    steps: [npm test -- --shard=$MATRIX_SHARD/2]

  trivy:
    stage: scan
    uses: scanners/trivy
    with: { severity: CRITICAL }

  deploy:
    stage: deploy
    manual: true
    when: { branches: [main] }
    secrets: [SSH_PRIVATE_KEY]
    uses: deploy/ssh
    with: { host: example.com, path: /srv/app }
`;

type Lint = { ok: boolean; errors: string[]; stages?: string[]; jobs?: { name: string; stage: string; image: string; needs: string[]; steps: string[]; manual: boolean; secrets: string[] }[] };

export function CiValidate() {
  const [yaml, setYaml] = useState(EXAMPLE);
  const [res, setRes] = useState<Lint | null>(null);
  const [busy, setBusy] = useState(false);

  const check = async () => {
    setBusy(true);
    setRes(await api<Lint>("/api/ci/lint", { method: "POST", body: { yaml } }).catch((e) => ({ ok: false, errors: [(e as Error).message] })));
    setBusy(false);
  };

  return (
    <CiSection active="/ci/validate">
      <div className="grid gap-6 lg:grid-cols-2">
        <Card
          title=".spikeloader.yml"
          icon="file"
          description="Paste a pipeline to check it before committing (or run spikeloader ci lint)."
          actions={
            <Button size="sm" variant="primary" icon="check" onClick={check} disabled={busy}>
              Validate
            </Button>
          }
        >
          <textarea
            value={yaml}
            onChange={(e) => {
              setYaml(e.target.value);
              setRes(null);
            }}
            onKeyDown={(e) => (e.ctrlKey || e.metaKey) && e.key === "Enter" && check()}
            spellCheck={false}
            className="h-[560px] w-full resize-y rounded-lg border border-border bg-bg p-3 font-mono text-xs leading-relaxed text-fg outline-none focus:border-accent"
          />
          <p className="mt-2 text-xs text-muted">Ctrl+Enter validates. Repository templates (.spikeloader/templates) are resolved by the CLI linter.</p>
        </Card>
        <Card title="Result" icon="pipelines">
          {!res ? (
            <Empty icon="file" title="Not validated yet">
              You get the expanded jobs (matrix and templates applied), their images and the dependency graph.
            </Empty>
          ) : !res.ok ? (
            <div className="flex flex-col gap-2">
              {res.errors.map((e) => (
                <Notice key={e} tone="danger">
                  {e}
                </Notice>
              ))}
            </div>
          ) : (
            <div className="flex flex-col gap-4">
              <Notice>
                <span className="text-ok">Valid pipeline</span> — {res.jobs?.length} jobs in {res.stages?.length} stages.
              </Notice>
              {res.stages?.map((st, i) => (
                <div key={st}>
                  <div className="mb-2 flex items-center gap-2 text-xs font-medium tracking-wide text-muted uppercase">
                    <span className="grid size-5 place-items-center rounded-full bg-surface-2 text-[10px]">{i + 1}</span>
                    {st}
                  </div>
                  <ul className="flex flex-col gap-2">
                    {res.jobs
                      ?.filter((j) => j.stage === st)
                      .map((j) => (
                        <li key={j.name} className={cx("rounded-lg border border-border px-3 py-2 text-xs")}>
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="font-mono text-sm font-medium">{j.name}</span>
                            <span className="rounded bg-surface-2 px-1.5 py-0.5 font-mono text-muted">{j.image}</span>
                            {j.manual && <span className="rounded bg-warn/10 px-1.5 py-0.5 text-warn">manual approval</span>}
                            {j.secrets.length > 0 && <span className="text-muted">secrets: {j.secrets.join(", ")}</span>}
                          </div>
                          <div className="mt-1.5 text-muted">{j.steps.join(" → ")}</div>
                          {j.needs.length > 0 && <div className="mt-1 text-muted">after {j.needs.join(", ")}</div>}
                        </li>
                      ))}
                  </ul>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>
    </CiSection>
  );
}

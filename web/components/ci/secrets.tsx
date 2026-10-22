"use client";

import { Card } from "../ui";
import type { Overview } from "./overview";
import { CiSection, SecretsEditor, usePoll } from "./shared";

export function CiGlobalSecrets() {
  const { data, reload } = usePoll<Overview>("/api/ci", 30_000);
  return (
    <CiSection active="/ci/secrets">
      <div className="max-w-3xl">
        <Card title="Global secrets" icon="key" description="Shared by every project. A project secret with the same name wins.">
          <SecretsEditor scope="_global" names={data?.globalSecrets ?? []} onChange={reload} />
        </Card>
      </div>
    </CiSection>
  );
}

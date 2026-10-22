import { Suspense } from "react";
import { CiRunView } from "@/components/ci/run";

export default function Page({ params }: PageProps<"/ci/runs/[id]">) {
  return (
    <Suspense fallback={<div className="text-sm text-muted">loading…</div>}>
      <CiRunView params={params} />
    </Suspense>
  );
}

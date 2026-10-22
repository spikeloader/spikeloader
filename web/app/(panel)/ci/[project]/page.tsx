import { Suspense } from "react";
import { CiProject } from "@/components/ci/project";

export default function Page({ params }: PageProps<"/ci/[project]">) {
  return (
    <Suspense fallback={<div className="text-sm text-muted">loading…</div>}>
      <CiProject params={params} />
    </Suspense>
  );
}

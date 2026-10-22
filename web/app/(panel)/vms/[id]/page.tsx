import { Suspense } from "react";
import { VmDetail } from "@/components/vm-detail";

export default function Page({ params }: PageProps<"/vms/[id]">) {
  return (
    <Suspense fallback={<div className="text-sm text-muted">loading…</div>}>
      <VmDetail params={params} />
    </Suspense>
  );
}

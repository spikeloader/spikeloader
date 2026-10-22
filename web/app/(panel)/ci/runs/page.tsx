import type { Metadata } from "next";
import { Suspense } from "react";
import { CiRuns } from "@/components/ci/runs";

export const metadata: Metadata = { title: "Runs" };

export default function Page() {
  return (
    <Suspense>
      <CiRuns />
    </Suspense>
  );
}

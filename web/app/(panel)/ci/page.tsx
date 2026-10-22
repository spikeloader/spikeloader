import type { Metadata } from "next";
import { CiOverview } from "@/components/ci/overview";

export const metadata: Metadata = { title: "Pipelines" };

export default function Page() {
  return <CiOverview />;
}

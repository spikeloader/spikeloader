import type { Metadata } from "next";
import { CiGlobalSecrets } from "@/components/ci/secrets";

export const metadata: Metadata = { title: "Global secrets" };

export default function Page() {
  return <CiGlobalSecrets />;
}

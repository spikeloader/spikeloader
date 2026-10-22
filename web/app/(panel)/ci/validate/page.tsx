import type { Metadata } from "next";
import { CiValidate } from "@/components/ci/validate";

export const metadata: Metadata = { title: "Validate a pipeline" };

export default function Page() {
  return <CiValidate />;
}

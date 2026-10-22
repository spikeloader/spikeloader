import type { Metadata } from "next";
import { CiLibrary } from "@/components/ci/library";

export const metadata: Metadata = { title: "Images & templates" };

export default function Page() {
  return <CiLibrary />;
}

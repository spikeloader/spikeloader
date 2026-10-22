import type { Metadata } from "next";
import { Infrastructure } from "@/components/infra";

export const metadata: Metadata = { title: "Infrastructure" };

export default function Page() {
  return <Infrastructure />;
}

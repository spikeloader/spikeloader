import { Suspense } from "react";
import { Nav } from "@/components/nav";
import { Providers } from "@/components/providers";
import { VERSION } from "@/lib/version.mjs";

export default function PanelLayout({ children }: LayoutProps<"/">) {
  return (
    <Providers>
      <Suspense>
        <Nav />
      </Suspense>
      <main className="mx-auto flex w-full max-w-7xl flex-1 flex-col gap-6 px-4 py-6 sm:px-6 sm:py-8">{children}</main>
      <footer className="mx-auto w-full max-w-7xl px-4 pb-6 text-xs text-muted sm:px-6">spikeloader v{VERSION} · CI/CD on Firecracker microVMs</footer>
    </Providers>
  );
}

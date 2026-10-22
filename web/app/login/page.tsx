import type { Metadata } from "next";
import { Suspense } from "react";
import { LoginForm } from "@/components/login-form";
import { LogoIcon, Wordmark } from "@/components/logo";

export const metadata: Metadata = { title: "Sign in" };

export default function LoginPage() {
  return (
    <main className="grid flex-1 place-items-center px-4 py-16">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3 text-center">
          <LogoIcon className="size-14" />
          <h1 className="text-2xl font-semibold tracking-tight">
            <Wordmark />
          </h1>
          <p className="text-sm text-muted">CI/CD on throwaway Firecracker microVMs.</p>
        </div>
        <div className="rounded-xl border border-border bg-surface p-5">
          <Suspense>
            <LoginForm />
          </Suspense>
        </div>
        <p className="mt-4 text-center text-xs text-muted">
          Generate an access link with <code className="font-mono text-fg">spikeloader web url</code>
        </p>
      </div>
    </main>
  );
}

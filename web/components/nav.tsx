"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Icon, type IconName } from "./icons";
import { LogoIcon, Wordmark } from "./logo";
import { api, useLab } from "./providers";
import { StatusBadge, cx } from "./ui";

const LINKS: { href: string; label: string; icon: IconName; match: string[] }[] = [
  { href: "/ci", label: "Pipelines", icon: "pipelines", match: ["/ci"] },
  { href: "/infra", label: "Infrastructure", icon: "server", match: ["/infra", "/vms"] },
];

export function Nav() {
  const path = usePathname();
  const { status, error } = useLab();
  const jobs = status?.vms.filter((v) => v.owner === "ci" && v.state !== "stopped").length ?? 0;

  const outer = error
    ? { state: "error", label: "panel not responding" }
    : !status
      ? { state: "offline", label: "loading…" }
      : status.outer.reachable
        ? { state: "online", label: jobs ? `runners online · ${jobs} job${jobs > 1 ? "s" : ""}` : "runners online · idle" }
        : status.outer.running
          ? { state: "starting", label: "runners starting…" }
          : { state: "off", label: "runners off · start on demand" };

  return (
    <header className="sticky top-0 z-30 border-b border-border bg-bg/85 backdrop-blur">
      <div className="mx-auto flex h-14 max-w-7xl items-center gap-3 px-4 sm:gap-6 sm:px-6">
        <Link href="/ci" className="flex shrink-0 items-center gap-2 font-semibold tracking-tight">
          <LogoIcon className="size-7" />
          <Wordmark className="hidden sm:inline" />
        </Link>
        <nav className="flex min-w-0 items-center gap-1 text-sm">
          {LINKS.map((l) => {
            const on = l.match.some((m) => path === m || path.startsWith(`${m}/`));
            return (
              <Link
                key={l.href}
                href={l.href}
                className={cx("inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 transition", on ? "bg-surface-2 font-medium text-fg" : "text-muted hover:text-fg")}
              >
                <Icon name={l.icon} className="size-4" />
                {l.label}
              </Link>
            );
          })}
        </nav>
        <div className="ml-auto flex shrink-0 items-center gap-2">
          <Link href="/infra" className="hidden md:block">
            <StatusBadge state={outer.state} label={outer.label} />
          </Link>
          <button
            onClick={async () => {
              await api("/api/auth/logout", { method: "POST" }).catch(() => {});
              location.replace("/login");
            }}
            className="inline-flex items-center gap-1.5 rounded-lg px-2 py-1.5 text-xs text-muted hover:bg-surface-2 hover:text-fg"
            title="Sign out"
          >
            <Icon name="logout" className="size-4" />
            <span className="hidden lg:inline">Sign out</span>
          </button>
        </div>
      </div>
    </header>
  );
}

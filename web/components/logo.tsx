import { useId } from "react";

/** spikeloader icon (same drawing as brand/spikeloader-icon.svg). */
export function LogoIcon({ className }: { className?: string }) {
  const id = useId();
  return (
    <svg viewBox="0 0 512 512" className={className} aria-hidden>
      <defs>
        <linearGradient id={id} gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="512" y2="512">
          <stop offset="0" stopColor="#ff8f45" />
          <stop offset="1" stopColor="#e2510a" />
        </linearGradient>
      </defs>
      <rect width="512" height="512" rx="116" fill={`url(#${id})`} />
      <path d="M393.9 251.7A140 140 0 1 1 280.3 138.1" fill="none" stroke="#fff" strokeWidth="44" strokeLinecap="round" />
      <path
        d="M306 56 150 300h92l-40 156 166-252h-94z"
        fill="#fff"
        stroke={`url(#${id})`}
        strokeWidth="22"
        strokeLinejoin="round"
        paintOrder="stroke"
        transform="translate(284 230) rotate(28) scale(0.72) translate(-256 -256)"
      />
    </svg>
  );
}

export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={className}>
      spike<span className="text-accent">loader</span>
    </span>
  );
}

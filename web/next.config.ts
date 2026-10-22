import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  cacheComponents: true,
  partialPrefetching: true,
  // the panel runs commands as root: no framing (clickjacking), no MIME sniffing, no referrer leaks
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "X-Frame-Options", value: "DENY" },
          { key: "Content-Security-Policy", value: "frame-ancestors 'none'" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "no-referrer" },
        ],
      },
    ];
  },
  // screens merged into Infrastructure / Images & templates
  async redirects() {
    return [
      { source: "/outer", destination: "/infra", permanent: false },
      { source: "/jobs", destination: "/infra", permanent: false },
      { source: "/ci/images", destination: "/ci/library", permanent: false },
    ];
  },
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;

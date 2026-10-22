import type { MetadataRoute } from "next";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "spikeloader",
    short_name: "spikeloader",
    description: "Self-hosted CI/CD: every job runs in its own Firecracker microVM.",
    id: "/",
    start_url: "/",
    scope: "/",
    display: "standalone",
    background_color: "#0f0f11",
    theme_color: "#0f0f11",
    categories: ["developer", "utilities"],
    icons: [
      { src: "/icons/icon-192.png", sizes: "192x192", type: "image/png" },
      { src: "/icons/icon-512.png", sizes: "512x512", type: "image/png" },
      { src: "/icons/maskable-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
      { src: "/icon.svg", sizes: "any", type: "image/svg+xml" },
    ],
  };
}

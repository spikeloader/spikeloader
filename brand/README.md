# spikeloader brand

<img src="png/spikeloader-512.png" width="96" alt="icon">

**Concept**: a *spike* (spark/lightning bolt — the Firecracker burst) breaking out of a *loader* ring
(the near-instant boot of the microVMs). The ring is open at the top right so the tip of the bolt
can cross it.

## Files

| File | Use |
|------|-----|
| `spikeloader-icon.svg` | master icon (rounded tile) — SVG favicon, nav, login |
| `spikeloader-icon-maskable.svg` | full-bleed version (no corners) for maskable and iOS icons |
| `png/spikeloader-512.png`, `-192.png` | rasterized icons |
| `png/spikeloader-maskable-512.png` | PWA maskable icon |
| `png/spikeloader-banner.png` | 1280×640 banner (README, social media) |

The drawing also exists as a React component in `web/components/logo.tsx` — keep the two in sync.

## Colors

| Token | Light | Dark | Use |
|-------|-------|------|-----|
| icon gradient | `#ff8f45 → #e2510a` | same | icon only |
| accent | `#e8590c` | `#ff7a2f` | primary actions, "loader" in the wordmark |
| background | `#f6f6f4` | `#0f0f11` | pages, PWA |
| danger | `#c92a2a` | `#ff6b6b` | destructive actions |

## Wordmark

`spike` in the text color + `loader` in the accent, Geist semibold, always lowercase and one word.
Write the name as **spikeloader** (not "SpikeLoader" or "Spike Loader").

## Regenerating the assets

```bash
npm i --prefix /tmp/pp puppeteer-core
node brand/build-assets.cjs /tmp/pp/node_modules/puppeteer-core
```

Generates the PNGs in `brand/png/`, `web/app/{favicon.ico,icon.svg,apple-icon.png}` and `web/public/icons/`
from the SVGs (uses the installed Google Chrome to rasterize).

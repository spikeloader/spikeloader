// Generates the brand assets from the SVGs in brand/.
// usage: node brand/build-assets.cjs <path to puppeteer-core>
// Requires Google Chrome (uses headless Chrome to rasterize).
const fs = require("node:fs");
const path = require("node:path");
const puppeteer = require(process.argv[2] ?? "puppeteer-core");

const BRAND = __dirname;
const WEB = path.join(BRAND, "..", "web");
const icon = fs.readFileSync(path.join(BRAND, "spikeloader-icon.svg"), "utf8");
const maskable = fs.readFileSync(path.join(BRAND, "spikeloader-icon-maskable.svg"), "utf8");
const uri = (svg) => "data:image/svg+xml;base64," + Buffer.from(svg).toString("base64");

async function rasterize(page, svg, size) {
  await page.setViewport({ width: size, height: size, deviceScaleFactor: 1 });
  await page.setContent(`<body style="margin:0;background:transparent"><img src="${uri(svg)}" width=${size} height=${size} style="display:block">`);
  await page.waitForFunction(() => document.images[0].complete);
  return page.screenshot({ omitBackground: true, type: "png" });
}

/** ICO with a PNG payload (supported by every current browser). */
function ico(pngs) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(pngs.length, 4);
  let offset = 6 + 16 * pngs.length;
  const entries = pngs.map(({ size, data }) => {
    const e = Buffer.alloc(16);
    e.writeUInt8(size >= 256 ? 0 : size, 0);
    e.writeUInt8(size >= 256 ? 0 : size, 1);
    e.writeUInt16LE(1, 4);
    e.writeUInt16LE(32, 6);
    e.writeUInt32LE(data.length, 8);
    e.writeUInt32LE(offset, 12);
    offset += data.length;
    return e;
  });
  return Buffer.concat([header, ...entries, ...pngs.map((p) => p.data)]);
}

const BANNER = `<!doctype html><html><head>
<link href="https://fonts.googleapis.com/css2?family=Geist:wght@400;600;700&family=Geist+Mono:wght@500&display=block" rel="stylesheet">
<style>
  body{margin:0;width:1280px;height:640px;display:flex;align-items:center;justify-content:center;
    background:radial-gradient(120% 90% at 85% 10%,#2a160b 0%,#0f0f11 55%);font-family:Geist,sans-serif;color:#ececee}
  .row{display:flex;align-items:center;gap:56px}
  h1{font-size:112px;font-weight:700;letter-spacing:-0.04em;margin:0;line-height:1}
  h1 span{color:#ff7a2f}
  p{font-size:32px;color:#a1a0a8;margin:22px 0 0;max-width:720px;line-height:1.35}
  code{font-family:'Geist Mono',monospace;font-size:22px;color:#ff9a5c;background:#1f1f23;border:1px solid #2a2a2f;
    padding:10px 16px;border-radius:10px;display:inline-block;margin-top:34px}
</style></head><body><div class="row">
  <img src="${uri(icon)}" width="260" height="260">
  <div><h1>spike<span>loader</span></h1>
  <p>Self-hosted CI/CD — every job in its own Firecracker microVM.</p>
  <code>$ spikeloader ci run my-app -f</code></div>
</div></body></html>`;

(async () => {
  const browser = await puppeteer.launch({ executablePath: "/usr/bin/google-chrome", headless: true });
  const page = await browser.newPage();
  const out = {};
  for (const s of [16, 32, 48, 180, 192, 512]) out[s] = await rasterize(page, icon, s);
  const mask512 = await rasterize(page, maskable, 512);
  const mask180 = await rasterize(page, maskable, 180); // iOS rounds the corners itself

  const write = (file, data) => {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, data);
    console.log("  " + path.relative(path.join(BRAND, ".."), file));
  };
  write(path.join(BRAND, "png", "spikeloader-512.png"), out[512]);
  write(path.join(BRAND, "png", "spikeloader-192.png"), out[192]);
  write(path.join(BRAND, "png", "spikeloader-maskable-512.png"), mask512);
  write(path.join(WEB, "app", "favicon.ico"), ico([16, 32, 48].map((size) => ({ size, data: out[size] }))));
  write(path.join(WEB, "app", "icon.svg"), icon);
  write(path.join(WEB, "app", "apple-icon.png"), mask180);
  write(path.join(WEB, "public", "icons", "icon-192.png"), out[192]);
  write(path.join(WEB, "public", "icons", "icon-512.png"), out[512]);
  write(path.join(WEB, "public", "icons", "maskable-512.png"), mask512);

  await page.setViewport({ width: 1280, height: 640, deviceScaleFactor: 1 });
  await page.setContent(BANNER, { waitUntil: "networkidle0" });
  await page.evaluate(() => document.fonts.ready);
  write(path.join(BRAND, "png", "spikeloader-banner.png"), await page.screenshot({ type: "png" }));
  await browser.close();
})();

import fs from "node:fs";
import path from "node:path";

/** Version shown in the footer: the repository's VERSION file (read at build/render time on the server). */
export const VERSION = (() => {
  try {
    return fs.readFileSync(path.join(process.cwd(), "..", "VERSION"), "utf8").trim();
  } catch {
    return "dev";
  }
})();

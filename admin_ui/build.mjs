// Builds the admin UI into dist/: tsc (src/*.ts -> dist/*.js), sass (src/styles.scss -> dist/styles.css)
// a copy of src/index.html and of the d3 bundle (loaded as a global by index.html). Run via `npm run build` (run_build.sh does this).
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, readdirSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const dist = join(root, "dist");
const bin = (name) => join(root, "node_modules", ".bin", name);

// Clean previous artifacts but keep the .gitkeep placeholder that makes `go:embed` work on a fresh checkout.
mkdirSync(dist, { recursive: true });
for (const f of readdirSync(dist)) {
  if (f !== ".gitkeep") rmSync(join(dist, f), { recursive: true, force: true });
}

execFileSync(bin("tsc"), ["-p", root], { stdio: "inherit" });
execFileSync(bin("sass"), ["--no-source-map", "--style=compressed", join(root, "src/styles.scss"), join(dist, "styles.css")], { stdio: "inherit" });
copyFileSync(join(root, "node_modules/d3/dist/d3.min.js"), join(dist, "d3.min.js"));
copyFileSync(join(root, "src/index.html"), join(dist, "index.html"));
console.log("Built admin_ui/dist");

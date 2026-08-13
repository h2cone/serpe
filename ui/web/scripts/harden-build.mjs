import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const clientRoot = path.resolve(here, "../build/client");
const htmlPath = path.join(clientRoot, "index.html");
const assetRoot = path.join(clientRoot, "assets");
let html = await readFile(htmlPath, "utf8");
const writes = [];

html = html.replace(
  /<script([^>]*)>([\s\S]*?)<\/script>/g,
  (element, attributes, source) => {
    if (/\bsrc\s*=/.test(attributes)) return element;
    if (!source.trim()) return "";
    const digest = createHash("sha256").update(source).digest("hex").slice(0, 16);
    const filename = `serpe-bootstrap-${digest}.js`;
    writes.push(writeFile(path.join(assetRoot, filename), source, "utf8"));
    // An external module with `async` may race the following serialized route
    // data scripts. Module scripts already defer by default, which preserves
    // parser order and keeps hydration deterministic after externalization.
    const orderedAttributes = attributes.replace(/\sasync(?:="")?/gi, "");
    return `<script${orderedAttributes} src="/assets/${filename}"></script>`;
  },
);

await Promise.all(writes);
if (/<script(?![^>]*\bsrc\s*=)[^>]*>/i.test(html)) {
  throw new Error("production build still contains an inline script");
}
if (/unsafe-(?:eval|inline)|https?:\/\//i.test(html)) {
  throw new Error("production shell contains a forbidden remote or unsafe source");
}
await writeFile(htmlPath, html, "utf8");

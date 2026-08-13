/**
 * Production Node entry: reverse-proxy /api/* to the Serpe API, rest to the
 * public React Router SPA build.
 * Dev uses Vite proxy instead (see vite.config.ts).
 *
 * Run after `pnpm run build`: `node --import tsx server.ts` or `pnpm start`.
 */
import { once } from "node:events";
import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";
import { pipeline } from "node:stream/promises";
import { fileURLToPath } from "node:url";
import { apiOrigin } from "./api-origin.ts";

const API_ORIGIN = apiOrigin();
const PORT = Number(process.env.PORT ?? 3000);

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const clientRoot = path.resolve(__dirname, "build/client");
const clientRootPrefix = `${clientRoot}${path.sep}`;

const uiCSP = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' blob:",
  "font-src 'self'",
  "connect-src 'self'",
  "object-src 'none'",
  "base-uri 'none'",
  "frame-ancestors 'none'",
  "form-action 'self'",
  "worker-src 'none'",
].join("; ");

function secureUIResponse(
  res: import("node:http").ServerResponse,
  html: boolean,
) {
  res.setHeader("Content-Security-Policy", uiCSP);
  res.setHeader("Referrer-Policy", "no-referrer");
  res.setHeader("X-Frame-Options", "DENY");
  res.setHeader("X-Content-Type-Options", "nosniff");
  res.setHeader("Permissions-Policy", "camera=(), microphone=(), geolocation=()");
  if (html) {
    res.setHeader("Cache-Control", "no-store");
  }
}

const contentTypes = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".ico", "image/x-icon"],
  [".jpeg", "image/jpeg"],
  [".jpg", "image/jpeg"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".map", "application/json; charset=utf-8"],
  [".png", "image/png"],
  [".svg", "image/svg+xml"],
  [".webp", "image/webp"],
  [".woff", "font/woff"],
  [".woff2", "font/woff2"],
]);

async function serveUI(
  req: import("node:http").IncomingMessage,
  res: import("node:http").ServerResponse,
) {
  if (req.method !== "GET" && req.method !== "HEAD") {
    secureUIResponse(res, false);
    res.statusCode = 405;
    res.setHeader("Allow", "GET, HEAD");
    res.end("Method Not Allowed");
    return;
  }
  let pathname: string;
  try {
    pathname = decodeURIComponent(new URL(req.url ?? "/", "http://ui.invalid").pathname);
  } catch {
    secureUIResponse(res, false);
    res.statusCode = 400;
    res.end("Bad Request");
    return;
  }
  if (pathname.includes("\0") || pathname.includes("\\")) {
    secureUIResponse(res, false);
    res.statusCode = 400;
    res.end("Bad Request");
    return;
  }
  const relative = pathname.replace(/^\/+/, "");
  let filename = path.resolve(clientRoot, relative);
  if (filename !== clientRoot && !filename.startsWith(clientRootPrefix)) {
    secureUIResponse(res, false);
    res.statusCode = 400;
    res.end("Bad Request");
    return;
  }
  let info = await stat(filename).catch(() => null);
  if (!info?.isFile()) {
    if (pathname.startsWith("/assets/") || pathname.startsWith("/.vite/")) {
      secureUIResponse(res, false);
      res.statusCode = 404;
      res.end("Not Found");
      return;
    }
    filename = path.join(clientRoot, "index.html");
    info = await stat(filename);
  }
  const extension = path.extname(filename).toLowerCase();
  const html = extension === ".html";
  secureUIResponse(res, html);
  res.statusCode = 200;
  res.setHeader("Content-Type", contentTypes.get(extension) ?? "application/octet-stream");
  res.setHeader("Content-Length", info.size);
  if (!html && pathname.startsWith("/assets/")) {
    res.setHeader("Cache-Control", "public, max-age=31536000, immutable");
  }
  if (req.method === "HEAD") {
    res.end();
    return;
  }
  await pipeline(createReadStream(filename), res);
}

const server = createServer(async (req, res) => {
  try {
    if (req.url?.startsWith("/api/")) {
      await proxyToGo(req, res);
      return;
    }
    await serveUI(req, res);
  } catch (err) {
    console.error(err);
    if (!res.headersSent) {
      res.statusCode = 500;
      res.end("Internal Server Error");
    }
  }
});

async function proxyToGo(
  req: import("node:http").IncomingMessage,
  res: import("node:http").ServerResponse,
) {
  const target = new URL(req.url ?? "/", API_ORIGIN);
  const headers: Record<string, string> = {};
  const hopByHop = new Set([
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
  ]);
  for (const [k, v] of Object.entries(req.headers)) {
    const lower = k.toLowerCase();
    if (v && lower !== "host" && lower !== "cookie" && !hopByHop.has(lower)) {
      headers[k] = Array.isArray(v) ? v.join(", ") : v;
    }
  }
  const abort = new AbortController();
  const close = () => {
    if (!res.writableEnded) abort.abort();
  };
  res.once("close", close);
  const init: RequestInit = {
    method: req.method,
    headers,
    signal: abort.signal,
    redirect: "manual",
  };
  if (req.method !== "GET" && req.method !== "HEAD") {
    // The Go API applies endpoint-specific caps; this front door additionally
    // prevents an unbounded duplicate body allocation before forwarding.
    const chunks: Buffer[] = [];
    let bytes = 0;
    for await (const chunk of req) {
      const data = typeof chunk === "string" ? Buffer.from(chunk) : chunk;
      bytes += data.length;
      if (bytes > (8 << 20)) {
        res.statusCode = 413;
        res.setHeader("Cache-Control", "no-store");
        res.setHeader("X-Content-Type-Options", "nosniff");
        res.end("Request Too Large");
        return;
      }
      chunks.push(data);
    }
    init.body = Buffer.concat(chunks);
  }
  try {
    const upstream = await fetch(target, init);
    res.statusCode = upstream.status;
    upstream.headers.forEach((value: string, key: string) => {
      if (hopByHop.has(key.toLowerCase())) return;
      res.setHeader(key, value);
    });
    if (!upstream.body) {
      res.end();
      return;
    }
    const reader = upstream.body.getReader();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!res.write(value)) await once(res, "drain");
    }
    res.end();
  } finally {
    res.off("close", close);
  }
}

server.listen(PORT, () => {
  console.log(`serpe web on :${PORT} (api → ${API_ORIGIN})`);
});

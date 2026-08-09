/**
 * Production Node entry: reverse-proxy /api/* to Go, rest to RR7 handler.
 * Dev uses Vite proxy instead (see vite.config.ts).
 *
 * Run after `npm run build`: `node --import tsx server.ts` or compile first.
 * Prefer: `npx tsx server.ts` in production-like runs.
 */
import { createRequestListener } from "@react-router/node";
import { createServer } from "node:http";
import { pathToFileURL } from "node:url";
import path from "node:path";
import { fileURLToPath } from "node:url";

const GO_ORIGIN = process.env.SERPE_GO_ORIGIN ?? "http://127.0.0.1:8080";
const PORT = Number(process.env.PORT ?? 3000);

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const buildPath = pathToFileURL(path.join(__dirname, "build/server/index.js")).href;
const build = await import(buildPath);

const rr = createRequestListener({
  build: build as never,
  mode: process.env.NODE_ENV ?? "production",
});

const server = createServer(async (req, res) => {
  try {
    if (req.url?.startsWith("/api/")) {
      await proxyToGo(req, res);
      return;
    }
    await rr(req, res);
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
  const target = new URL(req.url ?? "/", GO_ORIGIN);
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(req.headers)) {
    if (v && k.toLowerCase() !== "host") {
      headers[k] = Array.isArray(v) ? v.join(", ") : v;
    }
  }
  const init: RequestInit = { method: req.method, headers };
  if (req.method !== "GET" && req.method !== "HEAD") {
    // Collect body (simple V1; streaming proxy for large uploads not needed)
    const chunks: Buffer[] = [];
    for await (const chunk of req) {
      chunks.push(typeof chunk === "string" ? Buffer.from(chunk) : chunk);
    }
    init.body = Buffer.concat(chunks);
  }
  const upstream = await fetch(target, init);
  res.statusCode = upstream.status;
  upstream.headers.forEach((value: string, key: string) => {
    if (key.toLowerCase() === "transfer-encoding") return;
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
    res.write(value);
  }
  res.end();
}

server.listen(PORT, () => {
  console.log(`serpe ui on :${PORT} (api → ${GO_ORIGIN})`);
});

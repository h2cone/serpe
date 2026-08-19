/**
 * Start the local web UI and/or serpe-server from ui/web.
 *
 * The API binary is built and run with the repository root as cwd so `go`
 * sees go.mod and the server's default session CWD is the repo, not ui/web.
 */
import { spawn, spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export function webRootFrom(scriptDir = here) {
  return path.resolve(scriptDir, "..");
}

export function repoRootFrom(scriptDir = here) {
  return path.resolve(webRootFrom(scriptDir), "../..");
}

export function parseDevArgs(argv) {
  let api = true;
  let web = true;
  for (const arg of argv) {
    if (arg === "--api-only") {
      web = false;
      continue;
    }
    if (arg === "--web-only") {
      api = false;
      continue;
    }
    throw new Error(`unknown argument: ${arg}`);
  }
  if (!api && !web) {
    throw new Error("use only one of --api-only or --web-only");
  }
  return { api, web };
}

export function reactRouterCli(webRoot = webRootFrom()) {
  const require = createRequire(path.join(webRoot, "package.json"));
  return path.join(
    path.dirname(require.resolve("@react-router/dev/package.json")),
    "bin.js",
  );
}

export function assertRepoRoot(
  repoRoot,
  exists = (file) => existsSync(file),
) {
  if (!exists(path.join(repoRoot, "go.mod"))) {
    throw new Error(`serpe: expected go.mod at ${repoRoot}`);
  }
  if (!exists(path.join(repoRoot, "cmd", "serpe-server"))) {
    throw new Error(`serpe: expected cmd/serpe-server at ${repoRoot}`);
  }
}

export function apiBinaryName(platform = process.platform) {
  return platform === "win32" ? "serpe-server.exe" : "serpe-server";
}

export function childSpawnOptions(role, { cwd, env } = {}) {
  // Do not set windowsHide. CREATE_NO_WINDOW detaches the child from the
  // console, so Ctrl+C would not reach serpe-server or the Vite process.
  if (role === "api") {
    return { cwd, env, stdio: ["ignore", "inherit", "inherit"] };
  }
  return { cwd, env, stdio: "inherit" };
}

export function killProcessTree(
  pid,
  {
    platform = process.platform,
    spawnFn = spawn,
    spawnSyncFn = spawnSync,
    killFn = (id, signal) => process.kill(id, signal),
    force = false,
    sync = false,
  } = {},
) {
  if (!Number.isInteger(pid) || pid <= 0) return;
  if (platform === "win32") {
    const run = sync ? spawnSyncFn : spawnFn;
    run("taskkill", ["/pid", String(pid), "/t", "/f"], {
      stdio: "ignore",
      windowsHide: true,
    });
    return;
  }
  try {
    killFn(pid, force ? "SIGKILL" : "SIGTERM");
  } catch {
    // already gone
  }
}

function waitForExit(child) {
  if (child.exitCode != null || child.signalCode != null) {
    return Promise.resolve(child.exitCode ?? 1);
  }
  return new Promise((resolve) => {
    const done = (code) => resolve(code ?? 1);
    child.once("exit", done);
    child.once("error", () => done(1));
  });
}

function spawnLogged(spawnFn, command, args, options, log, label) {
  const child = spawnFn(command, args, options);
  child.once("error", (err) => {
    const missing = err && err.code === "ENOENT";
    if (missing && label === "api") {
      log("serpe api: go not found on PATH");
      return;
    }
    if (missing && label === "web") {
      log("serpe web: node executable not found");
      return;
    }
    log(`serpe ${label}: ${err.message}`);
  });
  return child;
}

/**
 * @returns {Promise<number>} process exit code
 */
export async function runDevStack({
  api = true,
  web = true,
  scriptDir = here,
  platform = process.platform,
  env = process.env,
  execPath = process.execPath,
  spawnFn = spawn,
  spawnSyncFn = spawnSync,
  killFn,
  webCli,
  tmpDir,
  createTmp = (prefix) => mkdtemp(prefix),
  removeTmp = (dir) => rm(dir, { recursive: true, force: true }),
  log = console.error,
  processRef = process,
} = {}) {
  const webRoot = webRootFrom(scriptDir);
  const repoRoot = repoRootFrom(scriptDir);
  if (api) assertRepoRoot(repoRoot);

  const children = [];
  let outcome = 0;
  let shuttingDown = false;
  let interrupts = 0;
  let builtDir;
  const treeKill = killFn ?? ((id, signal) => process.kill(id, signal));

  const stop = (code, force = false) => {
    if (!shuttingDown) {
      shuttingDown = true;
      outcome = code;
    } else if (!force) {
      return;
    }
    for (const child of children) {
      killProcessTree(child.pid, {
        platform,
        spawnFn,
        spawnSyncFn,
        killFn: treeKill,
        force,
        sync: true,
      });
    }
  };

  const onInterrupt = () => {
    interrupts += 1;
    if (interrupts === 1) log("serpe: stopping (Ctrl+C)");
    stop(0, interrupts > 1);
  };
  processRef.on("SIGINT", onInterrupt);
  processRef.on("SIGTERM", onInterrupt);
  processRef.on("SIGHUP", onInterrupt);

  try {
    if (api) {
      builtDir =
        tmpDir ?? (await createTmp(path.join(os.tmpdir(), "serpe-dev-")));
      const binary = path.join(builtDir, apiBinaryName(platform));
      log(`serpe api: go build -o ${binary} ./cmd/serpe-server`);
      const build = spawnLogged(
        spawnFn,
        "go",
        ["build", "-o", binary, "./cmd/serpe-server"],
        { cwd: repoRoot, env, stdio: "inherit" },
        log,
        "api",
      );
      const buildCode = await waitForExit(build);
      if (buildCode !== 0) return buildCode;

      log(`serpe api: ${binary}`);
      children.push(
        spawnLogged(
          spawnFn,
          binary,
          [],
          childSpawnOptions("api", { cwd: repoRoot, env }),
          log,
          "api",
        ),
      );
    }

    if (web) {
      const cli = webCli ?? reactRouterCli(webRoot);
      log("serpe web: react-router dev");
      children.push(
        spawnLogged(
          spawnFn,
          execPath,
          [cli, "dev"],
          childSpawnOptions("web", { cwd: webRoot, env }),
          log,
          "web",
        ),
      );
    }

    if (children.length === 0) return 0;

    await Promise.all(
      children.map(async (child) => {
        const code = await waitForExit(child);
        if (!shuttingDown) stop(code);
      }),
    );
    return outcome;
  } finally {
    processRef.removeListener("SIGINT", onInterrupt);
    processRef.removeListener("SIGTERM", onInterrupt);
    processRef.removeListener("SIGHUP", onInterrupt);
    if (builtDir && !tmpDir) {
      await removeTmp(builtDir).catch(() => {});
    }
  }
}

const invokedDirectly =
  Boolean(process.argv[1]) &&
  import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href;

if (invokedDirectly) {
  try {
    process.exitCode = await runDevStack(parseDevArgs(process.argv.slice(2)));
  } catch (err) {
    console.error(err instanceof Error ? err.message : err);
    process.exitCode = 1;
  }
}

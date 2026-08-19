import { EventEmitter } from "node:events";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  apiBinaryName,
  assertRepoRoot,
  childSpawnOptions,
  killProcessTree,
  parseDevArgs,
  reactRouterCli,
  repoRootFrom,
  runDevStack,
  webRootFrom,
} from "./dev.mjs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(scriptDir, "..");
const repoRoot = path.resolve(scriptDir, "../../..");

afterEach(() => {
  vi.restoreAllMocks();
});

describe("parseDevArgs", () => {
  it("starts both processes by default", () => {
    expect(parseDevArgs([])).toEqual({ api: true, web: true });
  });

  it("selects one side", () => {
    expect(parseDevArgs(["--api-only"])).toEqual({ api: true, web: false });
    expect(parseDevArgs(["--web-only"])).toEqual({ api: false, web: true });
  });

  it("rejects unknown or contradictory flags", () => {
    expect(() => parseDevArgs(["--watch"])).toThrow(/unknown argument/);
    expect(() => parseDevArgs(["--api-only", "--web-only"])).toThrow(
      /use only one/,
    );
  });
});

describe("layout", () => {
  it("resolves ui/web and the Go module from this scripts directory", () => {
    expect(webRootFrom(scriptDir)).toBe(webRoot);
    expect(repoRootFrom(scriptDir)).toBe(repoRoot);
    expect(assertRepoRoot(repoRoot)).toBeUndefined();
    expect(() => assertRepoRoot(webRoot)).toThrow(/go\.mod/);
  });

  it("points the web CLI at @react-router/dev's bin", () => {
    expect(reactRouterCli(webRoot).replaceAll("\\", "/")).toMatch(
      /@react-router\/dev\/bin\.js$/,
    );
  });

  it("names the Windows API binary with .exe", () => {
    expect(apiBinaryName("win32")).toBe("serpe-server.exe");
    expect(apiBinaryName("linux")).toBe("serpe-server");
  });

  it("keeps children on the console so Ctrl+C reaches both", () => {
    const api = childSpawnOptions("api", { cwd: "/repo", env: {} });
    const web = childSpawnOptions("web", { cwd: "/web", env: {} });
    expect(api.windowsHide).toBeUndefined();
    expect(web.windowsHide).toBeUndefined();
    expect(api.stdio).toEqual(["ignore", "inherit", "inherit"]);
    expect(web.stdio).toBe("inherit");
  });
});

describe("killProcessTree", () => {
  it("uses taskkill /T /F on Windows", () => {
    const spawnFn = vi.fn();
    const spawnSyncFn = vi.fn();
    killProcessTree(4321, { platform: "win32", spawnFn });
    expect(spawnFn).toHaveBeenCalledWith(
      "taskkill",
      ["/pid", "4321", "/t", "/f"],
      { stdio: "ignore", windowsHide: true },
    );
    killProcessTree(4321, { platform: "win32", spawnFn, spawnSyncFn, sync: true });
    expect(spawnSyncFn).toHaveBeenCalledWith(
      "taskkill",
      ["/pid", "4321", "/t", "/f"],
      { stdio: "ignore", windowsHide: true },
    );
  });

  it("sends SIGTERM then SIGKILL on Unix", () => {
    const killFn = vi.fn();
    killProcessTree(9, { platform: "linux", spawnFn: vi.fn(), killFn });
    expect(killFn).toHaveBeenCalledWith(9, "SIGTERM");
    killProcessTree(9, {
      platform: "linux",
      spawnFn: vi.fn(),
      killFn,
      force: true,
    });
    expect(killFn).toHaveBeenCalledWith(9, "SIGKILL");
  });

  it("ignores missing pids", () => {
    const spawnFn = vi.fn();
    killProcessTree(undefined as unknown as number, {
      platform: "win32",
      spawnFn,
    });
    expect(spawnFn).not.toHaveBeenCalled();
  });
});

type FakeChild = EventEmitter & {
  pid: number;
  exitCode: number | null;
  signalCode: NodeJS.Signals | null;
};

function fakeChild(pid: number): FakeChild {
  const child = new EventEmitter() as FakeChild;
  child.pid = pid;
  child.exitCode = null;
  child.signalCode = null;
  return child;
}

function finish(child: FakeChild, code: number) {
  child.exitCode = code;
  child.emit("exit", code);
}

describe("runDevStack", () => {
  it("builds the API from the repo root, then starts API and web", async () => {
    const tmpDir = path.join(repoRoot, "tmp-serpe-dev");
    const binary = path.join(tmpDir, apiBinaryName("linux"));
    const build = fakeChild(11);
    const api = fakeChild(12);
    const web = fakeChild(13);
    const spawnFn = vi
      .fn()
      .mockReturnValueOnce(build)
      .mockReturnValueOnce(api)
      .mockReturnValueOnce(web);
    const signals = new EventEmitter();

    const running = runDevStack({
      scriptDir,
      platform: "linux",
      env: {},
      execPath: "/usr/bin/node",
      spawnFn,
      webCli: "/cli/bin.js",
      tmpDir,
      processRef: signals as unknown as NodeJS.Process,
      log: () => {},
    });

    expect(spawnFn).toHaveBeenNthCalledWith(
      1,
      "go",
      ["build", "-o", binary, "./cmd/serpe-server"],
      expect.objectContaining({ cwd: repoRoot }),
    );
    finish(build, 0);
    await vi.waitFor(() => expect(spawnFn).toHaveBeenCalledTimes(3));

    expect(spawnFn).toHaveBeenNthCalledWith(
      2,
      binary,
      [],
      expect.objectContaining({ cwd: repoRoot }),
    );
    expect(spawnFn).toHaveBeenNthCalledWith(
      3,
      "/usr/bin/node",
      ["/cli/bin.js", "dev"],
      expect.objectContaining({ cwd: webRoot }),
    );

    finish(web, 0);
    finish(api, 0);
    await expect(running).resolves.toBe(0);
  });

  it("does not start processes when the API build fails", async () => {
    const build = fakeChild(21);
    const spawnFn = vi.fn().mockReturnValue(build);
    const running = runDevStack({
      api: true,
      web: true,
      scriptDir,
      platform: "linux",
      env: {},
      spawnFn,
      webCli: "/cli/bin.js",
      tmpDir: path.join(repoRoot, "tmp-serpe-dev"),
      processRef: new EventEmitter() as unknown as NodeJS.Process,
      log: () => {},
    });
    finish(build, 2);
    await expect(running).resolves.toBe(2);
    expect(spawnFn).toHaveBeenCalledTimes(1);
  });

  it("stops the sibling when one process exits", async () => {
    const tmpDir = path.join(repoRoot, "tmp-serpe-dev");
    const build = fakeChild(31);
    const api = fakeChild(32);
    const web = fakeChild(33);
    const spawnFn = vi
      .fn()
      .mockReturnValueOnce(build)
      .mockReturnValueOnce(api)
      .mockReturnValueOnce(web);
    const killFn = vi.fn();
    const running = runDevStack({
      scriptDir,
      platform: "linux",
      env: {},
      execPath: "/usr/bin/node",
      spawnFn,
      killFn,
      webCli: "/cli/bin.js",
      tmpDir,
      processRef: new EventEmitter() as unknown as NodeJS.Process,
      log: () => {},
    });
    finish(build, 0);
    await vi.waitFor(() => expect(spawnFn).toHaveBeenCalledTimes(3));
    finish(api, 7);
    finish(web, 1);
    await expect(running).resolves.toBe(7);
    expect(killFn).toHaveBeenCalledWith(33, "SIGTERM");
  });

  it("stops both children when SIGINT arrives", async () => {
    const tmpDir = path.join(repoRoot, "tmp-serpe-dev");
    const build = fakeChild(51);
    const api = fakeChild(52);
    const web = fakeChild(53);
    const spawnFn = vi
      .fn()
      .mockReturnValueOnce(build)
      .mockReturnValueOnce(api)
      .mockReturnValueOnce(web);
    const killFn = vi.fn();
    const signals = new EventEmitter();
    const running = runDevStack({
      scriptDir,
      platform: "linux",
      env: {},
      execPath: "/usr/bin/node",
      spawnFn,
      killFn,
      webCli: "/cli/bin.js",
      tmpDir,
      processRef: signals as unknown as NodeJS.Process,
      log: () => {},
    });
    finish(build, 0);
    await vi.waitFor(() => expect(spawnFn).toHaveBeenCalledTimes(3));
    signals.emit("SIGINT");
    expect(killFn).toHaveBeenCalledWith(52, "SIGTERM");
    expect(killFn).toHaveBeenCalledWith(53, "SIGTERM");
    finish(api, 0);
    finish(web, 0);
    await expect(running).resolves.toBe(0);
  });

  it("skips the API when --web-only is set", async () => {
    const web = fakeChild(41);
    const spawnFn = vi.fn().mockReturnValue(web);
    const running = runDevStack({
      api: false,
      web: true,
      scriptDir,
      platform: "linux",
      env: {},
      execPath: "/usr/bin/node",
      spawnFn,
      webCli: "/cli/bin.js",
      processRef: new EventEmitter() as unknown as NodeJS.Process,
      log: () => {},
    });
    expect(spawnFn.mock.calls[0][0]).toBe("/usr/bin/node");
    finish(web, 0);
    await expect(running).resolves.toBe(0);
  });
});

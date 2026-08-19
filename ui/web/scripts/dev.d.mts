import type { ChildProcess, SpawnOptions } from "node:child_process";

export function webRootFrom(scriptDir?: string): string;
export function repoRootFrom(scriptDir?: string): string;
export function parseDevArgs(argv: readonly string[]): {
  api: boolean;
  web: boolean;
};
export function reactRouterCli(webRoot?: string): string;
export function assertRepoRoot(
  repoRoot: string,
  exists?: (file: string) => boolean,
): void;
export function apiBinaryName(platform?: NodeJS.Platform): string;
export function childSpawnOptions(
  role: "api" | "web",
  options?: { cwd?: string; env?: NodeJS.ProcessEnv },
): SpawnOptions;
export function killProcessTree(
  pid: number,
  options?: {
    platform?: NodeJS.Platform;
    spawnFn?: (
      command: string,
      args: readonly string[],
      options: SpawnOptions,
    ) => ChildProcess;
    spawnSyncFn?: (
      command: string,
      args: readonly string[],
      options: SpawnOptions,
    ) => unknown;
    killFn?: (pid: number, signal: NodeJS.Signals) => void;
    force?: boolean;
    sync?: boolean;
  },
): void;

export function runDevStack(options?: {
  api?: boolean;
  web?: boolean;
  scriptDir?: string;
  platform?: NodeJS.Platform;
  env?: NodeJS.ProcessEnv;
  execPath?: string;
  spawnFn?: (
    command: string,
    args: readonly string[],
    options: SpawnOptions,
  ) => Pick<ChildProcess, "pid" | "exitCode" | "signalCode" | "once">;
  spawnSyncFn?: (
    command: string,
    args: readonly string[],
    options: SpawnOptions,
  ) => unknown;
  killFn?: (pid: number, signal: NodeJS.Signals) => void;
  webCli?: string;
  tmpDir?: string;
  createTmp?: (prefix: string) => Promise<string>;
  removeTmp?: (dir: string) => Promise<void>;
  log?: (message: string) => void;
  processRef?: Pick<NodeJS.Process, "on" | "removeListener">;
}): Promise<number>;

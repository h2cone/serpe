const workspaceKey = "serpe:workspace";

export function loadWorkspace(): string {
  try {
    return localStorage.getItem(workspaceKey) ?? "";
  } catch {
    return "";
  }
}

export function saveWorkspace(cwd: string) {
  try {
    const value = cwd.trim();
    if (value) localStorage.setItem(workspaceKey, value);
    else localStorage.removeItem(workspaceKey);
  } catch {
    // private mode or blocked storage
  }
}

export function pendingPromptKey(id: string) {
  return `serpe:pending:${id}`;
}

export function titleFromPrompt(prompt: string) {
  const line = prompt.trim().split(/\r?\n/, 1)[0] ?? "";
  if (line.length <= 72) return line;
  return `${line.slice(0, 71).trimEnd()}…`;
}

export function shortPath(value: string) {
  const normalized = value.replace(/\\/g, "/").replace(/\/+$/, "");
  const parts = normalized.split("/").filter(Boolean);
  if (parts.length <= 2) return value;
  return parts.slice(-2).join("/");
}

export const DEFAULT_GO_ORIGIN = "http://127.0.0.1:8080";

/** Resolve the single backend-origin setting used by dev, SSR, and proxying. */
export function goOrigin(
  env: Record<string, string | undefined> = process.env,
): string {
  return env.SERPE_GO_ORIGIN?.trim() || DEFAULT_GO_ORIGIN;
}

export const DEFAULT_API_ORIGIN = "http://127.0.0.1:8080";

/** Resolve the single API-origin setting used by dev, SSR, and proxying. */
export function apiOrigin(
  env: Record<string, string | undefined> = process.env,
): string {
  return env.SERPE_API_ORIGIN?.trim() || DEFAULT_API_ORIGIN;
}

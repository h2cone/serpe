import type { Config } from "@react-router/dev/config";

export default {
  // Protected data can only be requested after a user supplies a tab-memory
  // bearer token. The production server therefore serves a public SPA shell;
  // route loaders execute in that browser tab and add Authorization headers.
  ssr: false,
} satisfies Config;

import type { Config } from "@react-router/dev/config";

export default {
  // The production server serves a public SPA shell; route loaders execute
  // in the browser and fetch the API directly.
  ssr: false,
} satisfies Config;

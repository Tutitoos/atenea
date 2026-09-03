import type { Config } from "@react-router/dev/config";

export default {
  ssr: false,
  appDirectory: "app",
  buildDirectory: "build",
  basename: "/",
} satisfies Config;

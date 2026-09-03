import { reactRouter } from "@react-router/dev/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [tailwindcss(), reactRouter()],
  resolve: { tsconfigPaths: true },
  server: {
    proxy: {
      "/api": {
        target: process.env.ATENEA_DASHBOARD_API || "http://127.0.0.1:8788",
        changeOrigin: false,
      },
    },
  },
  build: {
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("node_modules/recharts")) return "charts";
          if (id.includes("node_modules/@xyflow") || id.includes("node_modules/dagre")) return "graphs";
          return undefined;
        },
      },
    },
  },
});

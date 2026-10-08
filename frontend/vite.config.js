import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// While developing (`npm run dev`) the page is served by Vite on the laptop,
// but the API lives in the cluster. This forwards every /api request to the
// real console so the page has live data to show.
//
// CONSOLE_URL is where Traefik can be reached from the laptop. It is read
// from the environment so that no address is written in this repository:
//
//   CONSOLE_URL=http://<gateway address> npm run dev
//
// The Host header tells Traefik which route the request is for.
const consoleUrl = process.env.CONSOLE_URL || "http://console.cluster.local";
const consoleHost = process.env.CONSOLE_HOST || "console.cluster.local";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: consoleUrl,
        headers: { Host: consoleHost },
      },
    },
  },
});

import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  build: {
    cssCodeSplit: false,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8092",
    },
  },
});

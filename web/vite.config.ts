import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  build: {
    cssCodeSplit: false,
    rollupOptions: {
      output: {
        entryFileNames: "assets/app.js",
        assetFileNames: (asset) =>
          asset.name === "style.css" ? "assets/styles.css" : "assets/[name][extname]",
      },
    },
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8092",
    },
  },
});

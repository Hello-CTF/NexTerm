import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  root: new URL(".", import.meta.url).pathname,
  publicDir: "public",
  plugins: [react()],
  build: {
    outDir: "../_site",
    emptyOutDir: true,
  },
});

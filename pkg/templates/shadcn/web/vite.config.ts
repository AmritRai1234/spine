import path from "path"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Expose SPINE_URL / SPINE_API_KEY from .env files to the client bundle
  envPrefix: ["VITE_", "SPINE_"],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    proxy: {
      "/tables": "http://localhost:8080",
      "/events": "http://localhost:8080",
      "/schema": "http://localhost:8080",
      "/health": "http://localhost:8080",
    },
    // Spine's WebSocket endpoint cannot be proxied transparently; the client
    // connects to ws://localhost:8080/ws directly (see src/lib/spine.ts).
  },
})

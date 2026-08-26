import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/emit': 'http://localhost:8080',
      '/health': 'http://localhost:8080',
      '/schema': 'http://localhost:8080',
      '/tables': 'http://localhost:8080',
      '/events': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true }
    }
  }
})

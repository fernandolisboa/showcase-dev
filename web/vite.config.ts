import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The Go binary embeds this build (go:embed) — see internal/web/web.go. Output
// goes into the Go embed package so a single `make build` produces the whole app.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    // During `vite dev`, proxy API + health to the Go control plane.
    proxy: {
      '/healthz': 'http://localhost:8080',
      '/api': 'http://localhost:8080',
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/setupTests.ts',
  },
})

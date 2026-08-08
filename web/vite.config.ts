import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The build lands in internal/ui/dist, which is committed and embedded into the
// binary. Node is therefore a BUILD-time dependency only: `go build` alone still
// produces a server with nothing to install beside it, which is the property the
// rest of this project is arranged around.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
    // Hashed asset names, so a deploy cannot serve a stale bundle out of a
    // cache. emptyOutDir keeps the committed directory from accumulating the
    // old ones.
    assetsDir: 'assets',
  },
  server: {
    // `npm run dev` talks to a darak running on :8080, so the interface can be
    // worked on without rebuilding the Go binary. Going through the proxy keeps
    // everything same-origin, which is what makes the session cookie work here.
    proxy: {
      '/api': 'http://localhost:8080',
      '/s': 'http://localhost:8080',
    },
  },
})

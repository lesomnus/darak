import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Where the dev server sends /api and /s.
//
//   unset            a darak on the host, the plain `npm run dev` case
//   http://darak:8080  the container in deploy/dev, reached by service name
const apiOrigin = process.env.DARAK_ORIGIN ?? 'http://localhost:8080'

// Bind mounts do not carry inotify on Docker Desktop (macOS, Windows), so a
// containerised dev server there sees no file change and never reloads. Polling
// is the only thing that works and it costs CPU, so it is opt-in: deploy/dev
// passes VITE_POLL=1 through from the environment.
const poll = process.env.VITE_POLL === '1'

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
    // The interface can be worked on without rebuilding the Go binary. Going
    // through the proxy keeps everything same-origin, which is what makes the
    // session cookie work here.
    proxy: {
      // The trailing slashes are load-bearing.
      //
      // Vite matches a string proxy key with startsWith, so a key of '/s' also
      // matches '/src/main.tsx' -- the app's entry module, handed to the Go
      // server, which correctly answers "404 page not found" for it. The page
      // then loads with an empty <div id="root"> and no error that names the
      // cause. That was true of this config from the day it was written; it
      // took putting the dev server in a container, where :8080 has no
      // interface to fall back on, for anything to look wrong.
      //
      // '/s/' cannot match '/src/' -- the third character differs -- and every
      // API route is '/api/<something>', so neither key gives anything up.
      //
      // changeOrigin stays FALSE, which is not Vite's shorthand default. The
      // server builds a share link out of the request's Host header
      // (shares.go), so rewriting Host to the target hands back a URL naming
      // the target: on the host that is localhost:8080 instead of :5173, and in
      // deploy/dev it is `http://darak:8080/s/…`, a compose service name that
      // resolves nowhere outside the compose network. Leaving Host alone makes
      // the issued link the one the browser is already on.
      '/api/': { target: apiOrigin, changeOrigin: false },
      '/s/': { target: apiOrigin, changeOrigin: false },
    },
    watch: poll ? { usePolling: true, interval: 300 } : undefined,
  },
})

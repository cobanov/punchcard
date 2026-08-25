import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The build output goes straight into the Go package that embeds it, so there
// is exactly one place a built asset can live and no copy step to forget.
//
// Two entries, and the landing page is the reason for the second one. It is the
// first thing anyone sees and it has to paint before any JavaScript arrives, so
// it does not import the app: it ships its own small stylesheet and about sixty
// lines of script that draw the punch card, and nothing else.
//
// `dev` proxies /v1 to a locally running server, which is what makes hot reload
// usable — the browser talks to one origin, so the session cookie and the CSRF
// cookie behave exactly as they do in production.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: '../internal/http/webui/dist',
    emptyOutDir: true,
    rollupOptions: {
      input: {
        app: resolve(__dirname, 'index.html'),
        landing: resolve(__dirname, 'landing.html'),
      },
    },
  },
  server: {
    proxy: {
      '/v1': { target: 'http://localhost:8080', changeOrigin: false },
      '/docs': 'http://localhost:8080',
    },
  },
})

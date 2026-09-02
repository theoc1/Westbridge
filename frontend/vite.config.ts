import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Where `npm run dev` sends /api and /ws. Override when the Go backend runs
// somewhere other than the default WB_LISTEN.
const backend = process.env.WB_DEV_BACKEND ?? 'http://127.0.0.1:8080'

// The build lands in the Go tree so that internal/web/assets can embed it
// into the binary via go:embed.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/web/assets/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      // The proxy forwards this page's Origin (localhost:5173) while
      // changeOrigin rewrites Host to the backend, so the handshake looks
      // cross-origin and the backend rejects it unless it is started with
      // WB_ALLOWED_ORIGINS=localhost:5173. See the README.
      '/ws': { target: backend, ws: true, changeOrigin: true },
    },
  },
})

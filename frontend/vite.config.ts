import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The build lands in the Go tree so that internal/web/assets can embed it
// into the binary via go:embed.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/web/assets/dist',
    emptyOutDir: true,
  },
})

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The dev server proxies /api to the operator's admin API, mirroring what nginx
// does in the production image. Run the operator locally with `make run`, or
// port-forward the admin API service:
//   kubectl -n stellarcd-system port-forward svc/stellarcd-admin-api 8080:8080
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    host: '0.0.0.0',
    proxy: {
      // No path rewrite: the admin API already serves the /api/v1 prefix.
      '/api': {
        target: process.env.ADMIN_API_URL || 'http://localhost:8080',
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    minify: 'terser'
  }
})

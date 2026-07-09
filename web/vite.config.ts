import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import fs from 'fs'
import path from 'path'
import { fileURLToPath } from 'url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const certPath = path.resolve(__dirname, '../certs/cert.pem')
const keyPath = path.resolve(__dirname, '../certs/key.pem')
const hasCerts = fs.existsSync(certPath) && fs.existsSync(keyPath)
const e2eBackendURL = process.env.VITE_E2E_BACKEND_URL?.trim()
const e2ePortRaw = process.env.VITE_E2E_PORT?.trim()
const viteCacheDir = process.env.VITE_CACHE_DIR?.trim()
const isE2EMode = Boolean(e2eBackendURL || e2ePortRaw)
const e2ePort = (() => {
  if (!e2ePortRaw) {
    return undefined
  }
  const port = Number(e2ePortRaw)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`Invalid VITE_E2E_PORT: ${e2ePortRaw}`)
  }
  return port
})()

export default defineConfig({
  plugins: [react()],
  ...(viteCacheDir ? { cacheDir: viteCacheDir } : {}),
  // Production build (`vite build`) output. The backend serves this directory
  // as the web UI root, so it goes to the repo root `static/` (one level up
  // from this `web/` root). emptyOutDir clears it before each build and, since
  // it sits outside the vite root, also silences Vite's out-of-root warning.
  // Only affects `vite build`; the dev server (`npm run dev`) is untouched.
  build: {
    outDir: '../static',
    emptyOutDir: true,
  },
  server: {
    host: '0.0.0.0',
    port: e2ePort ?? (hasCerts ? 443 : 5173),
    strictPort: true,
    headers: {
      'Cache-Control': 'no-store',
    },
    ...(!isE2EMode && hasCerts && {
      https: {
        key: fs.readFileSync(keyPath),
        cert: fs.readFileSync(certPath),
      },
    }),
    allowedHosts: ['.fanlv.fun'],
    fs: {
      deny: ['.env', '.env.*', '*.{crt,pem}', '*.key', '**/.git/**', '/etc/**', '**/certs/**'],
    },
    proxy: {
      '/api': {
        target: e2eBackendURL || 'http://localhost:8090',
        changeOrigin: true,
      },
    },
  },
})

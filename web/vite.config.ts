import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import fs from 'fs'
import path from 'path'

const certPath = path.resolve(__dirname, '../certs/cert.pem')
const keyPath = path.resolve(__dirname, '../certs/key.pem')
const hasCerts = fs.existsSync(certPath) && fs.existsSync(keyPath)
const e2eBackendURL = process.env.VITE_E2E_BACKEND_URL?.trim()
const e2ePortRaw = process.env.VITE_E2E_PORT?.trim()
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

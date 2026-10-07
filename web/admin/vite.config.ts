import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  // The dev server proxies the API to a running jarvisd's admin listener.
  const backendUrl = env.JARVISD_ADMIN_URL || 'http://localhost:7710'

  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    // jarvisd embeds web/admin/dist (web/admin/embed.go). The build goes to dist/ui so that
    // emptyOutDir only ever cleans that subdirectory and leaves the committed
    // dist/placeholder.html alone.
    build: {
      outDir: 'dist/ui',
      emptyOutDir: true,
    },
    // Component tests, matching jarvis-installer's setup. This repo had no
    // frontend test runner at all, which is why two wrong platform labels
    // shipped: the Hardware screen was never executed by anything.
    test: {
      globals: true,
      environment: 'jsdom',
      setupFiles: ['./tests/setup.ts'],
      include: ['src/**/*.test.tsx', 'src/**/*.test.ts'],
    },
    server: {
      // 7710 is jarvisd's admin listener itself; Vite stays on its default port.
      port: 5173,
      proxy: {
        '/api': backendUrl,
        '/health': backendUrl,
      },
    },
  }
})

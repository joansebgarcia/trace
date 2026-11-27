import solid from '@solidjs/vite-plugin'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [
    solid(),
  ],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8090',
    },
  },
  build: {
    outDir: 'build',
    emptyOutDir: true,
  },
})

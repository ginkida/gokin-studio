import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

// iter 910+: manual chunk splitting to close the perpetual "chunks larger
// than 500 KB" build warning. Before: single 1023 KB bundle (286 KB gzip)
// loaded fully on first paint. After: framework + heavy feature deps move
// into dedicated vendor chunks that the browser caches separately. Tiny
// patch releases of the app no longer invalidate the React/markdown
// vendor caches.
//
// Groupings chosen by SIZE × NATURALNESS:
//   - react-vendor: react + react-dom (foundation, stable across builds)
//   - markdown: react-markdown + rehype-highlight + highlight.js language
//     bundles + remark/rehype/mdast/micromark families (the single
//     largest dependency cluster — ~400 KB)
//   - terminal: @xterm/xterm + addon-fit
//   - icons: lucide-react (large icon set)
export default defineConfig({
  plugins: [react()],
  build: {
    rollupOptions: {
      output: {
        manualChunks: (id: string) => {
          if (!id.includes('node_modules')) return undefined
          if (id.includes('react-dom') || /node_modules\/react\//.test(id)) return 'react-vendor'
          if (
            id.includes('react-markdown') ||
            id.includes('rehype-highlight') ||
            id.includes('highlight.js') ||
            id.includes('hast-util') ||
            id.includes('mdast-util') ||
            id.includes('micromark') ||
            id.includes('remark') ||
            id.includes('unified')
          ) return 'markdown'
          if (id.includes('@xterm')) return 'terminal'
          if (id.includes('lucide-react')) return 'icons'
          return undefined
        },
      },
    },
    // App code lands in the main chunk; vendor splits keep each individual
    // chunk well under the warning threshold. Bump to 600 KB so we don't
    // see the warning if the main bundle grows slightly over 500 KB as
    // features are added.
    chunkSizeWarningLimit: 600,
  },
})

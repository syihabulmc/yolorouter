import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const backendTarget = process.env.VITE_BACKEND_TARGET || 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
    // Expose the dev server on all interfaces so the frontend is reachable
    // from other devices on the LAN (e.g. a phone used to test the mobile
    // layout). Fixed port so the proxy + bookmarks stay stable.
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/healthz': backendTarget,
      '/api': backendTarget,
      '/v1': backendTarget,
    },
  },
  css: {
    preprocessorOptions: {
      less: {
        // Single source of truth for the responsive breakpoint. Prepended to
        // every LESS compile — plain .less files AND <style lang="less"> blocks
        // in .vue — so @mobile-breakpoint resolves everywhere. Kept in sync
        // with the JS constant MOBILE_BREAKPOINT in composables/useIsMobile.ts.
        // CSS media queries can't read a JS/CSS variable, so the number lives
        // in these two places by design; use @mobile-breakpoint rather than
        // hard-coding 768px.
        additionalData: '@mobile-breakpoint: 768px;',
      },
    },
  },
})

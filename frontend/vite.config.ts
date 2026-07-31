import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const backendTarget = process.env.VITE_BACKEND_TARGET || 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
     host: '0.0.0.0', // 监听所有网络接口
    port: 5173,      // 保持端口不变
    proxy: {
      '/healthz': backendTarget,
      '/api': backendTarget,
      '/v1': backendTarget,
    },
  },
})

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 构建产物直接输出到 Go embed 目录；开发模式经代理访问后端。
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../server/internal/app/webdist',
    emptyOutDir: true,
    target: 'es2020',
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        // 只把 React 运行时单独切出来：它几乎每个页面都要用，独立成块后
        // 内容哈希稳定，可以长期缓存。
        //
        // MUI 之前是整包进一个 manual chunk（@mui/material + @mui/icons-material），
        // 结果是只有懒加载页面才会用到的组件与图标也被塞进首屏，
        // 首屏 JS 一度达到 386KB（gzip 后 116KB）。交给 Rollup 自动分块后，
        // 首屏只带真正立刻用得上的 MUI 部分，其余跟着页面按需下载。
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (
            id.includes('node_modules/react/') ||
            id.includes('node_modules/react-dom/') ||
            id.includes('node_modules/scheduler/') ||
            id.includes('node_modules/react-router/') ||
            id.includes('node_modules/react-router-dom/')
          ) {
            return 'react-vendor'
          }
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8000', changeOrigin: false },
    },
  },
})

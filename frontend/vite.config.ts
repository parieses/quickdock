import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import wails from "@wailsio/runtime/plugins/vite";

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [
    vue(),
    wails("./bindings"),
    // Wails v3 的次级 WebView2 窗口（剪贴板/笔记/命令面板）对带 crossorigin 的
    // 模块脚本做 CORS 校验，而 asset server 不返回 ACAO 头，导致模块脚本被拦截、
    // SPA 不挂载 → 白屏。主窗口是可信源不受影响。去掉 crossorigin 后即可在所有窗口加载。
    {
      name: "strip-crossorigin",
      enforce: "post",
      generateBundle(_: any, bundle: any) {
        for (const name in bundle) {
          if (name.endsWith(".html")) {
            const file = bundle[name]
            if (file && file.type === "asset" && typeof file.source === "string") {
              file.source = file.source.replace(/\s+crossorigin/g, "")
            }
          }
        }
      },
    },
  ],
  build: {
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('pinyin-pro')) return 'pinyin-pro'
          if (id.includes('@lucide/vue') || id.includes('lucide-vue')) return 'lucide'
        },
      },
    },
  },
});

import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import wails from "@wailsio/runtime/plugins/vite";
import { fileURLToPath } from "node:url";

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  // Vite 8 (rolldown) dev 模式下，运行时发现新依赖会触发二次预构建（re-optimize），
  // 该路径有已知的 `browserHash` 崩溃（error while updating dependencies）。
  // 预先声明依赖，让优化器在启动时一次性完成，避免中途 re-optimize。
  optimizeDeps: {
    include: [
      "vue",
      "vue-i18n",
      "pinia",
      "marked",
      // 公式渲染：AI 页是异步 chunk，katex 在该 chunk 里才被 import，
      // 不预声明会在第一次点开 AI 页时触发二次预构建（见本段开头说明）。
      "katex",
      "dompurify",
      // 代码块语法高亮：common 子集按需加载（AI 页用到才拉），必须预声明，
      // 否则首次渲染高亮代码块会触发二次预构建 → re-optimize 崩溃。
      "highlight.js/lib/common",
      // Mermaid 同样是「用到才加载」（动态 import，回答里出现 ```mermaid 才拉），
      // 必须在这里声明，否则首次渲染流程图时会触发二次预构建 → 页面请求 504。
      "mermaid",
      "pinyin-pro",
      "@lucide/vue",
      "@floating-ui/vue",
      "@wailsio/runtime",
      // 终端相关：xterm 体积大、加载晚（只在 SSH 窗口用到），
      // 不预声明会在首次打开 SSH 窗口时触发二次预构建 —— SshTerminal 导入的
      // 四个包必须全部列在这里，漏一个（曾漏 addon-search）就是打开 SSH 页时
      // 现发现依赖 → re-optimize → 旧 browserHash 的请求全部 504 → 页面打不开。
      "@xterm/xterm",
      "@xterm/addon-fit",
      "@xterm/addon-search",
      "@xterm/addon-unicode11",
    ],
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
    // 主 chunk 超过该体积才告警，避免大依赖拆分后频繁误报
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      // 多页入口：main = 主应用 SPA；plugin = 插件独立窗口轻量壳（不加载主 SPA，首开提速）
      input: {
        main: fileURLToPath(new URL("./index.html", import.meta.url)),
        plugin: fileURLToPath(new URL("./plugin.html", import.meta.url)),
      },
      output: {
        manualChunks(id: string) {
          // 仅对第三方依赖做分包，业务代码保持默认分组
          if (!id.includes('node_modules')) return
          if (id.includes('pinyin-pro')) return 'pinyin-pro'
          if (id.includes('@lucide/vue') || id.includes('lucide-vue')) return 'lucide'
          if (id.includes('vue-i18n')) return 'vue-i18n'
          if (id.includes('pinia')) return 'pinia'
          if (id.includes('marked')) return 'marked'
          if (id.includes('dompurify') || id.includes('isomorphic-dompurify')) return 'dompurify'
          // 重依赖独立分包：进对应页才按需拉，缩短首屏/进页白屏
          if (id.includes('@xterm')) return 'xterm'
          if (id.includes('katex')) return 'katex'
          if (id.includes('mermaid')) return 'mermaid'
          if (id.includes('highlight.js')) return 'highlight'
          if (id.includes('/vue/') || id.includes('vue-router')) return 'vue'
        },
      },
    },
  },
});

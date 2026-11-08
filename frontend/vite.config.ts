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
      // 图表库走 echarts/core 按需注册，四个子路径都是独立 ESM 入口。
      // 漏一个就会在首次打开图表页时「运行时发现新依赖」→ 二次预构建 →
      // 旧 browserHash 的请求全部 504（与上面 xterm 那次是同一个坑）。
      "echarts/core",
      "echarts/charts",
      "echarts/components",
      "echarts/renderers",
      // CodeMirror：utils/aiCodeEditor.ts 把核心包和 14 个语言包**全部**写成动态
      // import（首次打开笔记/配置编辑器才拉）。与上面 xterm、echarts 是同一个坑：
      // 运行时发现新依赖 → 二次预构建 → 504 → createCodeEditor 的 catch 吞掉异常，
      // 宿主 div 就成了永久空白；而预览走静态 marked 不受影响，于是表现为
      // 「源码视图一片空白、分屏时只有右边预览有内容」。这里必须把每一个动态
      // import 的裸模块说明符列全，漏一个语言包就会在打开对应文件时复现。
      "codemirror",
      "@codemirror/commands",
      "@codemirror/state",
      "@codemirror/search",
      "@codemirror/view",
      "@codemirror/language",
      "@lezer/highlight",
      "@codemirror/lang-javascript",
      "@codemirror/lang-vue",
      "@codemirror/lang-html",
      "@codemirror/lang-css",
      "@codemirror/lang-json",
      "@codemirror/lang-markdown",
      "@codemirror/lang-python",
      "@codemirror/lang-go",
      "@codemirror/lang-rust",
      "@codemirror/lang-java",
      "@codemirror/lang-cpp",
      "@codemirror/lang-php",
      "@codemirror/lang-sql",
      "@codemirror/lang-yaml",
      "@codemirror/legacy-modes/mode/properties",
      "@codemirror/legacy-modes/mode/shell",
      "@codemirror/legacy-modes/mode/toml",
      "@codemirror/legacy-modes/mode/nginx",
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
          // zrender 是 echarts 的绘图内核，必须一起分包；否则它会被打进主 chunk，
          // 首屏平白多背一份，图表页反而还要再拉一次 echarts。
          if (id.includes('echarts') || id.includes('zrender')) return 'echarts'
          if (id.includes('/vue/') || id.includes('vue-router')) return 'vue'
        },
      },
    },
  },
});

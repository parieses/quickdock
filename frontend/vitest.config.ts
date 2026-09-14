import { defineConfig } from 'vitest/config'

// 独立配置：主 vite.config.ts 带 wails 绑定插件与产物改写（strip-crossorigin），
// 单测只需能编译 TS，加载它们反而会引入 Wails runtime 与 bindings 依赖。
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
})

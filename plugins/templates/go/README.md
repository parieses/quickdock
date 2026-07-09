# Go 插件模板

## 开发步骤

1. 编辑 `main.go` 实现你的插件逻辑
2. 修改 `plugin.json` 更新插件信息
3. 编译插件：
   ```bash
   go build -o main.exe main.go
   ```
4. 打包为 zip：
   ```bash
   # 确保 main.exe + plugin.json 在 zip 根目录
   zip my-plugin.zip plugin.json main.exe
   ```
5. 在 QuickDock 插件管理页面拖入 zip 安装

## 通信协议

插件通过 stdin/stdout 使用 JSON-RPC 2.0 与主程序通信。

详见 `docs/plugin-dev-guide.md`。

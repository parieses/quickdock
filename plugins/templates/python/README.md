# Python 插件模板

## 开发步骤

1. 编辑 `main.py` 实现你的插件逻辑
2. 修改 `plugin.json` 更新插件信息
3. 打包为 zip：
   ```bash
   zip my-plugin.zip plugin.json main.py
   ```
4. 在 QuickDock 插件管理页面拖入 zip 安装

## 通信协议

插件通过 stdin/stdout 使用 JSON-RPC 2.0 与主程序通信。

详见 `docs/plugin-dev-guide.md`。

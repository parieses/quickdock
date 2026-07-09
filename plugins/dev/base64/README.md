# Base64 编码解码插件

一个纯前端的 Base64 编码/解码工具。

## 功能

- **编码**：将 UTF-8 文本编码为 Base64
- **解码**：将 Base64 解码为 UTF-8 文本
- **实时转换**：输入时自动计算，无需点击按钮
- **交换**：一键交换输入输出，方便快速验证
- **复制**：一键复制结果到剪贴板
- **快捷键**：`Ctrl+Enter` 执行、`Escape` 清空、`Tab` 切换焦点

## 架构

- **后端**（main.py）：处理命令面板的 encode/decode 请求
- **前端**（frontend/）：完整的可视化 UI，编码解码逻辑在浏览器内执行
- 支持通过 postMessage 与 QuickDock 主程序通信

## 使用方式

### 命令面板
1. 选中文本后，在命令面板搜索 "Base64"
2. 选择「Base64 编码」或「Base64 解码」
3. 结果返回在命令面板中显示

### 插件面板（开发中）
1. 在插件管理页面点击「打开界面」
2. 在嵌入的 UI 中输入文本
3. 自动实时转换

## 安装

```bash
# 打包
cd plugins/dev
powershell -File package-dev.ps1 base64

# 然后到 QuickDock 插件管理页面拖入 base64.zip
```

# QuickDock 插件开发目录

本目录用于开发和测试 QuickDock 插件。

## 目录结构

```
plugins/dev/
├── base64/               # Base64 编码解码插件
│   ├── plugin.json       # 插件清单
│   ├── main.py           # Python 后端
│   ├── frontend/         # 前端资源
│   │   ├── index.html    # UI 入口
│   │   ├── style.css     # 样式
│   │   └── app.js        # 逻辑（纯前端 Base64）
│   └── README.md
└── ...
```

## 开发流程

1. 在 `plugins/dev/` 下创建插件目录
2. 编写 `plugin.json` 和 `main.py`（或其他语言后端）
3. 按需创建 `frontend/` 前端资源
4. 打包为 zip 安装：

```bash
# Windows PowerShell
cd plugins/dev/base64
Compress-Archive -Path * -DestinationPath ../base64.zip
```

5. 在 QuickDock 插件管理页面拖入 zip 安装

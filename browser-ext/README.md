# QuickDock 密码库 · 浏览器扩展

把 QuickDock 里存的密码接到浏览器：**识别登录表单 → 一键填充 → 提交后提示保存**。
密码只存在本机 QuickDock 数据库里，扩展不联网、不上传任何数据。

## 支持

- Chrome / Edge（Chromium 102+，同一套 MV3 代码）
- 暂不支持 Firefox / Safari

## 安装（本地加载，不用上架商店）

### Chrome

1. 打开 `chrome://extensions`
2. 右上角打开「开发者模式」
3. 点「加载已解压的扩展程序」，选择本目录 `browser-ext/`
4. 每次浏览器启动时，Chrome 可能提示「请停用以开发者模式运行的扩展」——点 × 关掉即可，不影响功能

### Edge

同上，地址换成 `edge://extensions`，左下角「开发人员模式」→「加载解压缩的扩展」。

### 只装一份

**同一台浏览器里，扩展目录 和 解压出来的 zip 目录只能留一个。**两者都加载会变成两个独立扩展，同时往网页注入、同时弹建议条、同时抢 `9240` 端口。

manifest 里的 `key` 字段把扩展 ID 固定为 `gcpejbdijdnnhpfahfaejkhikojhfoej`，所以**换目录重装是同一个扩展**（设置和已填的连接密钥不会丢）；但已经装上的旧实例不会自动消失，需要先在 `chrome://extensions` 里把多余的那个删掉。

## 连接（只需一次）

1. 打开 QuickDock → 侧边栏「密码库」→ 右上角打开**浏览器集成**开关
2. 点「复制连接密钥」（端口默认 9240，会顺延）
3. 点扩展图标 → 设置 / 密钥（或直接在扩展详情里打开选项页）
4. 填端口、粘贴密钥 → 保存密钥

开关开着就一直有效，没有过期时间；关掉开关即吊销密钥并停止监听，扩展随之失效。
密钥存在扩展自己的存储里，网页脚本读不到。

## 用法

| 场景 | 行为 |
|---|---|
| 点进任意登录框 | 输入框下方弹出该网站的密码条目，点一下即可填入（用户名 + 密码一起填） |
| 点扩展图标 | 默认**只列当前网站关联的条目**；在搜索框里打字才跨站搜全库 |
| 首次在某网站登录 | **默认直接存入密码库**，右下角闪一条「已存入」（设置里可关成先询问） |
| 登录成功后页面跳走 | 新页面加载 1 秒后补弹保存提示 —— 提示不会跟着旧页面一起消失 |
| 改了密码再登录 | 提示「更新 QuickDock 里的密码？」（覆盖前一定问一次） |
| 密码没变 | 不打扰，只记一次使用 |

## 已知限制

- **注册 / 改密页面不填**：`autocomplete="new-password"` 的字段会主动跳过，否则会把旧密码写进新密码框
- **分步登录**：先用户名、后密码的流程（Google / 银行）需要你在第二屏点一下密码框触发建议条
- **Shadow DOM**：穿透两层（Salesforce 一类），更深的封装站点可能识别不到
- **iframe 内的表单**：能填，但条目按 iframe 自己的域名匹配，可能和主站不是同一条
- **端口**：QuickDock 默认监听 `9240`，被占用会顺延到 `9241~9249`；换端口后在设置页点「自动探测」
- **提交检测**：`submit` 事件、密码框/用户名框上的 Enter、以及文案像「登录 / 下一步 / Sign in」的按钮点击。纯 canvas 或完全自定义控件的站点可能识别不到
- **多标签同时登录**：待确认的提交只保留最后一份（3 分钟过期），并行登录两个站点时只会提示后一个

## 文件说明

```
manifest.json       MV3 清单（key 固定扩展 ID、host_permissions 限 127.0.0.1）
src/background.js   service worker：唯一持有 token、唯一发网络请求的地方
src/content.js      表单识别 / 填充 / 页面内建议条 / 保存提示
src/popup.*         点图标时的搜索与填充面板
src/options.*       端口探测 + 密钥校验
icons/              16/32/48/128 图标（由 build/appicon.png 缩放生成）
keys/               签名私钥（**不要分发**，不进 zip）
```

`keys/extension-private-key.pem` 是 `manifest.json` 里 `key` 对应的私钥。它只在两种场合用得着：往商店上传新版本时若需自行签名，以及以后要换 `key` 时核对配对关系。丢了不影响本地使用——manifest 里留下的是公钥，够 Chrome 算 ID 了。

## 打包与发布

```bash
python browser-ext/pack.py           # 源码有改动才出包
python browser-ext/pack.py --force   # 无条件重打
```

**版本号跟着桌面应用的 git tag 走**：`v0.0.48` → 包内 `0.0.48`。源码里 `manifest.json` 的 `version` 恒为 `0.0.0`，只是个占位，打包时按当前 tag 注入。所以不存在「扩展自己的版本号」，也不用记着改两处；`src/content.js` 的自检版本是运行时读 manifest 的，跟着自动对。

- 源码没改动 → 直接跳过，不产出只换了版本号的空包（判据是所有源文件的内容摘要）
- 产物 `browser-ext/dist/quickdock-vault-extension-v<tag>.zip`，已 gitignore
- 出包自查三件事：`manifest.json` 在 zip 根目录、没有反斜杠路径、`keys/` 没混进去
- 直接加载源码目录时版本显示 `0.0.0`，看到它就说明当前是未打包状态

**发版时由 CI 出包**：推 `v*` tag 后 `.github/workflows/release.yml` 里的 `build-extension` job 会跑 `pack.py --force`，校验包内版本与 tag 一致，把 zip 作为附件挂到 Release。所以本地通常不必再手动打一遍。

`keys/` 下的签名私钥不入库，需要自己另存一份。

## 安全边界

- token 只存在 `chrome.storage.local`，content script 拿不到（所有 API 调用都经 background）
- 与 QuickDock 的通信只走 `127.0.0.1`，且 QuickDock 侧会校验 `Origin: chrome-extension://...`
- 待确认的登录密码会暂存在 `chrome.storage.session`（**仅内存、不落盘、浏览器关闭即失效**，3 分钟过期，只有 background 能读）。
  这不是缓存优化，是必须的：登录成功会让整页卸载，只在那一瞬发消息十有八九丢掉，用户会觉得「登录完了密码没进库」
- 本扩展不申请 `cookies`、`webRequest`、`<all_urls>` 的读写权限，也不能读其他标签页内容

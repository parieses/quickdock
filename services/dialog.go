package services

// 本地文件/目录选择对话框。
//
// 为什么放在 AppService 而不是各业务域：这是宿主能力，不属于任何领域。
// 谁需要谁回指（services/sshx 已经持 s.App），不用在每个域各写一份；
// 也不该让前端用 <input type="file"> 代替——WebView2 里拿到的只有伪造路径
// 和一份内存副本，而 SFTP 上传必须在 Go 侧用本地绝对路径打开真实文件。
//
// 与 services/plugin 的 PickFilePath / PickFolderPath 的关系：那两个是
// 插件桥接专用，选中路径会记进 pickedPaths 白名单，供后续 qdReadFile 放行；
// 契约不同（有副作用），故没有合并。新增宿主级选择器一律加在这里，
// 不要再往业务域里塞第三份。

import (
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// dialogCancelled 判断这个错误是不是「用户按了取消」。
//
// 各平台的对话框实现都把取消当错误返回：Windows 走 internal/go-common-file-dialog
// 的 cfd.ErrorCancelled（文案 "cancelled by user"），darwin/linux 也各有 cancel 类错误。
// 那个包在 wails 的 internal/ 下，外部模块根本 import 不到，拿不到可比较的哨兵值，
// 只能按文案判定——这是本文件唯一一处字符串匹配，改动 wails 版本时值得复核。
//
// 为什么必须区分：不区分的话，用户在「另存为」里点一下取消，就会收到一条红色提示
// "cancelled by user"。取消是正常操作，不是失败；何况这行英文没人愿意读第二遍。
func dialogCancelled(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "cancel")
}

// dialogOwnerWindow 找出「用户此刻正在操作的那个窗口」。
//
// 不能直接用 MainWindow：SSH 终端跑在独立窗口里，把对话框挂在主窗口上时，
// 弹窗会开在已经最小化/被遮挡的主窗口上，用户看到的是「点了没反应」。
// 拿不到焦点窗口时返回 nil —— 平台实现会退化成无 owner 的顶层对话框，
// 虽然不居中于某个窗口，但至少一定会出现在最前面。
func (a *AppService) dialogOwnerWindow() application.Window {
	app := a.App()
	if app == nil {
		return nil
	}
	for _, w := range app.Window.GetAll() {
		if w != nil && w.IsFocused() {
			return w
		}
	}
	return nil
}

// SelectLocalFile 弹出「打开文件」对话框，返回用户选中的本地绝对路径。
//
// 用户取消时返回空串且 Code=0：取消是正常操作，不该让前端弹一条红色错误。
// 前端判断空串即可。
func (a *AppService) SelectLocalFile(title string) *ApiResult {
	app := a.App()
	if app == nil {
		return FailMsg("应用未就绪")
	}
	opts := &application.OpenFileDialogOptions{
		CanChooseFiles:       true,
		CanChooseDirectories: false,
		Title:                strings.TrimSpace(title),
		Window:               a.dialogOwnerWindow(),
	}
	path, err := app.Dialog.OpenFileWithOptions(opts).PromptForSingleSelection()
	if dialogCancelled(err) {
		return Ok("")
	}
	if err != nil {
		return Fail(err)
	}
	return Ok(path)
}

// SelectLocalFiles 弹出「打开文件（可多选）」对话框，返回选中的绝对路径列表。
// 取消或什么都不选时返回空数组且 Code=0（取消是正常操作）。
//
// 为什么单独一个方法而不是给 SelectLocalFile 加个开关：多选与单选在平台层是两条
// 不同的调用（PromptForMultipleSelection / PromptForSingleSelection），返回值形状
// 也不同，合成一个开关会让每个调用方都要先判类型。批量安装插件是第一个多选场景
// —— 一次拖/选五六个 zip 却只能装一个是明显的功能缺口。
func (a *AppService) SelectLocalFiles(title, filterName, pattern string) *ApiResult {
	app := a.App()
	if app == nil {
		return FailMsg("应用未就绪")
	}
	if strings.TrimSpace(filterName) == "" || strings.TrimSpace(pattern) == "" {
		filterName, pattern = "所有文件", "*.*"
	}
	opts := &application.OpenFileDialogOptions{
		CanChooseFiles:          true,
		CanChooseDirectories:    false,
		AllowsMultipleSelection: true,
		Title:                   strings.TrimSpace(title),
		Window:                  a.dialogOwnerWindow(),
		Filters:                 []application.FileFilter{{DisplayName: filterName, Pattern: pattern}},
	}
	paths, err := app.Dialog.OpenFileWithOptions(opts).PromptForMultipleSelection()
	if dialogCancelled(err) {
		return Ok([]string{})
	}
	if err != nil {
		return Fail(err)
	}
	if paths == nil {
		paths = []string{}
	}
	return Ok(paths)
}

// SelectLocalDirectory 弹出「选择目录」对话框，返回本地绝对路径；取消返回空串。
func (a *AppService) SelectLocalDirectory(title string) *ApiResult {
	app := a.App()
	if app == nil {
		return FailMsg("应用未就绪")
	}
	opts := &application.OpenFileDialogOptions{
		CanChooseFiles:       false,
		CanChooseDirectories: true,
		Title:                strings.TrimSpace(title),
		Window:               a.dialogOwnerWindow(),
	}
	path, err := app.Dialog.OpenFileWithOptions(opts).PromptForSingleSelection()
	if dialogCancelled(err) {
		return Ok("")
	}
	if err != nil {
		return Fail(err)
	}
	return Ok(path)
}

// SelectSavePath 弹出「另存为」对话框，defaultName 用作预填文件名。
// 返回完整目标路径；取消返回空串。
//
// 刻意不预填 Filters：SFTP 下载的对象可以是任意后缀（甚至没有后缀），
// 套一个「所有文件」以外的过滤器只会让用户在保存时莫名看不到目标文件。
func (a *AppService) SelectSavePath(title, defaultName string) *ApiResult {
	app := a.App()
	if app == nil {
		return FailMsg("应用未就绪")
	}
	opts := &application.SaveFileDialogOptions{
		Title:    strings.TrimSpace(title),
		Filename: strings.TrimSpace(defaultName),
		Window:   a.dialogOwnerWindow(),
	}
	path, err := app.Dialog.SaveFileWithOptions(opts).PromptForSingleSelection()
	if dialogCancelled(err) {
		return Ok("")
	}
	if err != nil {
		return Fail(err)
	}
	return Ok(path)
}

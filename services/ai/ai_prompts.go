package ai

// 提示词库（ai_prompts）的门面方法。
//
// 这里只做「校验 + 转调 db」，不掺业务：变量展开在前端插入时做（{{date}} 取的是
// 用户此刻的日期，后端算出来的时间可能因为重试/排队偏差几秒，而且插入后用户
// 还要能接着改），提示词本身也不参与模型调用——它就是一段被填进输入框的正文。
//
// 排序规则、内置条目的身份与「能改不能删」的边界都在 internal/db/prompt.go，
// 这里再写一遍只会让两处漂移。

import (
	"fmt"
	"strings"

	"quickdock/internal/db"
	"quickdock/services"
)

// AI 提示词的容量上限。给宽一点：模板里常嵌整段 diff 或长文，
// 真正要防的是「误把一整个文件粘进来」，不是限制表达。
const (
	promptNameMax     = 60
	promptContentMax  = 8000
	promptCategoryMax = 20
	promptCountMax    = 500
)

// AIGetPrompts 列出全部提示词（含内置）。
func (a *AIService) AIGetPrompts() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.App.DB.ListAIPrompts()
	if err != nil {
		return services.Fail(err)
	}
	if list == nil {
		list = []db.AIPrompt{}
	}
	return services.Ok(map[string]any{"prompts": list})
}

// AISavePrompt 新建或更新一条提示词，返回保存后的完整记录。
//
// 参数用散列的基本类型而不是入参结构体：Wails 绑定会把结构体生成成 class，
// 前端每次调用都得 new 一个再传，可读性反而更差（同类方法 AICreateSpace 也是这么写的）。
//
// 返回整条而不是只返回 id：前端要立刻把它插进列表并显示内置标记，
// 只给 id 就得再拉一次列表，慢一步还会与并发编辑打架。
func (a *AIService) AISavePrompt(id, name, content, category string, sort int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	name = strings.TrimSpace(name)
	content = strings.TrimSpace(content)
	// 这里再挡一次长度：前端输入框也有 maxlength，但那只是体验，
	// 真正的边界必须在后端（手改库、插件调用都会绕过前端）。
	if name == "" {
		return services.FailMsg("提示词名称不能为空")
	}
	if len([]rune(name)) > promptNameMax {
		return services.Fail(fmt.Errorf("名称最多 %d 个字", promptNameMax))
	}
	if content == "" {
		return services.FailMsg("提示词内容不能为空")
	}
	if len([]rune(content)) > promptContentMax {
		return services.Fail(fmt.Errorf("内容最多 %d 个字", promptContentMax))
	}
	if len([]rune(category)) > promptCategoryMax {
		return services.Fail(fmt.Errorf("分类最多 %d 个字", promptCategoryMax))
	}
	// 新建时才查总量：更新不该被「已经满了」挡住。
	if id == "" {
		list, err := a.App.DB.ListAIPrompts()
		if err != nil {
			return services.Fail(err)
		}
		if len(list) >= promptCountMax {
			return services.Fail(fmt.Errorf("提示词最多 %d 条，先删掉一些不用的", promptCountMax))
		}
	}

	saved, err := a.App.DB.SaveAIPrompt(&db.AIPrompt{
		ID:       id,
		Name:     name,
		Content:  content,
		Category: strings.TrimSpace(category),
		Sort:     sort,
	})
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(saved)
}

// AIDeletePrompt 删除一条自建提示词；内置条目会被拒绝（原因见 db.DeleteAIPrompt）。
func (a *AIService) AIDeletePrompt(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.FailMsg("缺少提示词 id")
	}
	if err := a.App.DB.DeleteAIPrompt(id); err != nil {
		return services.Fail(err)
	}
	return services.OkMsg(nil, "已删除")
}

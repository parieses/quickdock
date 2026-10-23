package env

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EEmbeddablePipBootstrap 端到端验证：用真实的 python.org embeddable 包
// 走一遍引导流程，确认装完之后 `python -m pip` 与 `pip install` 真的可用。
//
// 需要环境变量 QUICKDOCK_E2E_PYTHON_DIR 指向解包好的 embeddable 目录；
// 未设置则跳过（CI 上没有该包）。
func TestE2EEmbeddablePipBootstrap(t *testing.T) {
	dir := os.Getenv("QUICKDOCK_E2E_PYTHON_DIR")
	if dir == "" {
		t.Skip("未设置 QUICKDOCK_E2E_PYTHON_DIR，跳过端到端测试")
	}
	lay := detectPythonEmbedLayout(dir)
	if lay == nil {
		t.Fatalf("未能识别 embeddable 布局: %s", dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out, err := bootstrapPythonPip(ctx, lay, nil)
	t.Logf("引导输出:\n%s", out)
	if err != nil {
		t.Fatalf("bootstrapPythonPip 失败: %v", err)
	}
	if !pythonPipInstalled(lay.Exe) {
		t.Fatal("引导后 pip 仍不可用")
	}
	// 实装一个包并验证可导入
	inst, err := runPythonScript(ctx, lay, "-m", "pip", "install", "--quiet", "--index-url", "https://pypi.org/simple", "six")
	if err != nil {
		t.Fatalf("pip install 失败: %v\n%s", inst, out)
	}
	imp, err := runPythonScript(ctx, lay, "-c", "import six;print('six', six.__version__)")
	if err != nil {
		t.Fatalf("导入安装的包失败: %v", err)
	}
	t.Logf("导入验证: %s", imp)

	// venv 能力：embeddable 无内置 venv，应由 virtualenv 补齐
	if !pythonHasVirtualenv(lay.Exe) {
		t.Error("期望引导后已安装 virtualenv（用于补齐 embeddable 缺失的 venv 能力）")
	} else {
		venvDir := filepath.Join(t.TempDir(), "venv")
		vout, err := runPythonScript(ctx, lay, "-m", "virtualenv", venvDir)
		if err != nil {
			t.Errorf("virtualenv 创建虚拟环境失败: %v\n%s", err, vout)
		} else {
			t.Logf("virtualenv 创建成功: %s", strings.TrimSpace(firstLine(vout)))
		}
	}
}

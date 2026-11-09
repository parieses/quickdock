package ai

import (
	"path/filepath"
	"testing"
)

// 子 agent 被派到子目录时，读取范围仍应是编排的根。
//
// 拿子目录当范围的话，一个被派去改 src/api 的子 agent 连项目根的 go.mod 都读不到，
// 而「这次对话属于哪个项目」并没有因为它缩到子目录而改变。
func TestChildAgentScopeInheritsParent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "src", "api")
	parent := newAgentScope(nil, "conv", "goal", root, AIProfile{})
	if parent.scope != root {
		t.Fatalf("顶层范围应等于工作目录: got=%s want=%s", parent.scope, root)
	}
	child := newChildAgentScope(nil, "conv", "run", "node", sub, parent.scope, 1, AIProfile{})
	if child.scope != root {
		t.Fatalf("子 agent 范围应沿用父级: got=%s want=%s", child.scope, root)
	}
	if child.cwd != sub {
		t.Fatalf("子 agent 的工作目录仍应是它自己的: got=%s want=%s", child.cwd, sub)
	}
	// 没传父级范围时退回自己的 cwd，与阶段 1（无编排）的行为一致。
	bare := newChildAgentScope(nil, "conv", "run", "node", sub, "", 1, AIProfile{})
	if bare.scope != sub {
		t.Fatalf("缺省范围应退回自己的 cwd: got=%s want=%s", bare.scope, sub)
	}
}

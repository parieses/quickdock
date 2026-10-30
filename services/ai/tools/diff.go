package aitools

// 行级 unified diff（方案 A2）。
//
// 为什么不引 github.com/pmezard/go-difflib：这里只需要「改的是哪几行」这一种
// 输出，而那个库带来的是整套 Myers 实现 + 格式化器 + 依赖传递。用几十行代码换掉
// 一个长期依赖，在一个连 HTTP 客户端都自己写的项目里是划算的。
//
// 算法是标准 LCS 动态规划（不是 Myers）：file_edit 的改动通常只有几行，
// O(n*m) 在这个量级上完全够用，而 LCS 的表更容易读、不容易写错。
// 真正大的输入由 diffMaxLines 挡在外面——几千行的整份重写本来就该看 git diff，
// 而不是塞进一条聊天卡片。

import (
	"fmt"
	"strings"
)

const (
	// diffMaxLines 参与比较的单侧最大行数。超过就只报增删计数、不给逐行内容。
	//
	// 1000 行的 1000×1000 表是 100 万个 int32 = 4MB 内存，一次 file_edit 完全
	// 可以接受；再往上（整份重写的日志、数据文件）就没有展示价值了——
	// 用户看不完，而那些文件不该用 file_edit 改。
	diffMaxLines = 1000

	// diffContext unified 格式的上下文行数（与 git 默认一致）。
	diffContext = 3

	// diffMaxHunks 最多输出几个 hunk。
	//
	// 一份被大改的文件可能有几十个分散的 hunk，全画出来会把卡片撑到几百行。
	// 超出就截断并说明「还有 N 处未显示」——比默默少显示好。
	diffMaxHunks = 6
)

// opKind 一行在 diff 里的角色。
type opKind byte

const (
	opSame opKind = ' '
	opAdd  opKind = '+'
	opDel  opKind = '-'
)

type diffOp struct {
	kind opKind
	text string
}

// unifiedDiff 生成 old → new 的 unified diff 文本。
//
// 返回值：(diff 文本, 增加行数, 删除行数)。
// 输入过大时返回空文本与 0/0——调用方应当改用计数摘要（addedLines / removedLines
// 本来就有），而不是拿一个空字符串当「没有改动」。
func unifiedDiff(oldText, newText string) (string, int, int) {
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)
	if len(a) > diffMaxLines || len(b) > diffMaxLines {
		return "", 0, 0
	}
	ops := lcsOps(a, b)

	added, removed := 0, 0
	for _, o := range ops {
		switch o.kind {
		case opAdd:
			added++
		case opDel:
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return "", 0, 0
	}

	// 每个 op 之前已消耗的 old / new 行数，用于算 @@ 头里的行号。
	// 多算一位（len(ops)+1）是为了让「最后一个 op 之后」也有下标可用。
	oldBefore := make([]int, len(ops)+1)
	newBefore := make([]int, len(ops)+1)
	for k, o := range ops {
		oldBefore[k+1] = oldBefore[k]
		newBefore[k+1] = newBefore[k]
		if o.kind != opAdd {
			oldBefore[k+1]++
		}
		if o.kind != opDel {
			newBefore[k+1]++
		}
	}

	var sb strings.Builder
	hunks := 0
	truncated := false
	// cursor 已经输出到哪一行。下一个 hunk 不能从这里之前开始，否则两个 hunk
	// 会把同一段上下文画两遍（看起来像文件里有重复内容）。
	cursor := 0
	for k := 0; k < len(ops); {
		// 找到一个变化点
		if ops[k].kind == opSame {
			k++
			continue
		}
		// 本 hunk 覆盖到哪里：一直往后走到「连续相同行超过 context」为止。
		//
		// 用 lastChange 追踪最后一个变化位置，而不是「走完再回退相同行数」——
		// 后者在「相同行刚好超过阈值」时会把末尾的变化行一起退掉，
		// 表现出来就是 diff 里少了一行 +，而那正是改动本身。
		end := k
		lastChange := k
		for end < len(ops) {
			if ops[end].kind != opSame {
				lastChange = end
			} else if end-lastChange > diffContext {
				break
			}
			end++
		}
		// 末尾补上 context 行，收尾时不超过总长
		end = lastChange + 1 + diffContext
		if end > len(ops) {
			end = len(ops)
		}
		// 头部同样带 context，但不越过上一个 hunk
		start := k - diffContext
		if start < cursor {
			start = cursor
		}
		if start > k {
			start = k
		}

		if hunks >= diffMaxHunks {
			truncated = true
			break
		}
		hunks++

		oldCount := oldBefore[end] - oldBefore[start]
		newCount := newBefore[end] - newBefore[start]
		// 行号基准：hunk 从哪一行开始（1-based）。计数为 0 时用前一行号，
		// 这是 unified 格式的约定（`@@ -0,0 +1,5 @@` 表示「原来没有」）。
		oldStart := oldBefore[start] + 1
		newStart := newBefore[start] + 1
		if oldCount == 0 {
			oldStart = oldBefore[start]
		}
		if newCount == 0 {
			newStart = newBefore[start]
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, o := range ops[start:end] {
			sb.WriteByte(byte(o.kind))
			sb.WriteString(o.text)
			sb.WriteByte('\n')
		}
		k = end
		cursor = end
	}
	if truncated {
		fmt.Fprintf(&sb, "@@ 还有更多改动未显示（超过 %d 处）@@\n", diffMaxHunks)
	}
	return sb.String(), added, removed
}

// splitDiffLines 按行切分。
//
// 不用 strings.Split：它会把末尾换行变成最后多一个空元素，于是「文件末尾加了一行」
// 在 diff 里看起来像「改了两行」。按单个 \n 切、去掉末尾空段，与编辑器看到的行数一致。
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

// lcsOps 用 LCS 把两个序列对齐成「相同 / 新增 / 删除」的操作流。
//
// 表从后往前填（lcs[i][j] = a[i:] 与 b[j:] 的 LCS 长度），回溯时天然从前往后走，
// 输出的顺序就是文件顺序——反过来填表会得到一段倒序的 diff，得再 reverse 一次。
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// 用一维化没意义：回溯需要整张表；n,m ≤ 1000 时 4MB 是可接受的一次性开销。
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{opSame, a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			// 相等时优先输出删除：让「先删后加」成为固定顺序，
			// 否则同一处改动在不同调用里可能先 + 后 -，diff 看起来每次都不一样。
			ops = append(ops, diffOp{opDel, a[i]})
			i++
		default:
			ops = append(ops, diffOp{opAdd, b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{opDel, a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{opAdd, b[j]})
	}
	return ops
}

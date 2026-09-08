package plugin

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// 覆盖 boundedReadLine 的回归测试：
//  1. 正常行完整读回，truncated=false；
//  2. 超长无换行巨行不撑爆内存，返回前缀并 truncated=true（本次修复核心）；
//  3. 超长行被丢弃后不影响下一行的正确解析；
//  4. 最后一行无换行 + EOF 正确返回。
func TestBoundedReadLine(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		limit     int
		wantLines []string // 期望依次读到的行（不含换行；含截断前缀则以 <trunc>: 前缀标出）
	}{
		{
			name:  "正常单行",
			input: "hello\n",
			limit: 64,
			wantLines: []string{
				"hello",
			},
		},
		{
			name:  "超长无换行巨行截断且不污染下一行",
			input: strings.Repeat("A", 1000) + "|tail\nnext\n",
			limit: 100,
			wantLines: []string{
				"<trunc>:" + strings.Repeat("A", 100),
				"next",
			},
		},
		{
			name:  "超长行恰好跨内部缓冲(64KB)多块",
			input: strings.Repeat("B", 200*1024) + "\nlast\n",
			limit: 4096,
			wantLines: []string{
				"<trunc>:" + strings.Repeat("B", 4096),
				"last",
			},
		},
		{
			name:  "末行无换行读到EOF返回",
			input: "tail-no-newline",
			limit: 64,
			wantLines: []string{
				"tail-no-newline",
			},
		},
		{
			name:      "空输入立即EOF",
			input:     "",
			limit:     64,
			wantLines: []string{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bufio.NewReaderSize(strings.NewReader(c.input), 64*1024)
			got := []string{}
			for {
				line, truncated, err := boundedReadLine(r, c.limit)
				// 截断的前缀同样是有意义内容（调用方会尝试解析），统一收集校验。
				// 返回行含行尾 '\n'（兼容 \r\n），此处剥离后比对（readLoop 亦如此处理）。
				if len(line) > 0 {
					s := string(line)
					if n := len(s); s[n-1] == '\n' {
						s = s[:n-1]
						if n = len(s); n > 0 && s[n-1] == '\r' {
							s = s[:n-1]
						}
					}
					if truncated {
						s = "<trunc>:" + s
					}
					got = append(got, s)
				}
				if err != nil {
					if err != io.EOF {
						t.Fatalf("unexpected err: %v", err)
					}
					break
				}
			}
			if len(got) != len(c.wantLines) {
				t.Fatalf("行数不符: got %d lines %q, want %d lines %q", len(got), got, len(c.wantLines), c.wantLines)
			}
			for i := range got {
				if got[i] != c.wantLines[i] {
					t.Errorf("line %d 不符: got %q (len %d), want %q (len %d)",
						i, got[i], len(got[i]), c.wantLines[i], len(c.wantLines[i]))
				}
			}
		})
	}
}

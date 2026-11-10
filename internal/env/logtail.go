package env

import (
	"errors"
	"io"
	"os"
)

// readFileTail 读文件末尾最多 max 字节，供各运行时 LogGet 做尾部预览。
//
// 为什么不用 os.ReadFile：redis/traefik/php 这类长期运行的服务日志可涨到几百 MB，
// 全量读入只为截尾几 KB 是一次纯浪费的全文件磁盘读 + 内存尖峰。Stat 定位后
// 一次 ReadAt 只碰需要的那一段。
// 文件比 max 还短时整体返回；文件不存在时返回原样错误（调用方各自决定怎么提示）。
func readFileTail(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := st.Size() - int64(max)
	if start < 0 {
		start = 0
	}
	buf := make([]byte, st.Size()-start)
	if len(buf) > 0 {
		if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	return buf, nil
}

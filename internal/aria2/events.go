package aria2

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
)

// Notification 是 aria2 通过 WebSocket 主动推来的事件。
// 实测（1.37.0）格式：{"jsonrpc":"2.0","method":"aria2.onDownloadStart",
// "params":[{"gid":"f8b3..."}]}，无 id；带 id 的是我们自己的 RPC 响应，要忽略。
type Notification struct {
	Method string
	GID    string
}

const (
	maxEventBackoff = 30 * time.Second
	eventIdleTTL    = 5 * time.Minute
)

// Subscribe 连接 aria2 的 WS RPC 端点并消费通知，立即返回（内部起 goroutine）。
// onConnect(true/false) 让上层在 WS 在线时降轮询频率、断线时恢复；
// 断线后按 1s→30s 指数退避重连，ctx 取消即退出。
func Subscribe(ctx context.Context, cfg *Config, onConnect func(bool), onEvent func(Notification)) {
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			connected := pumpEvents(ctx, cfg, onConnect, onEvent)
			if ctx.Err() != nil {
				return
			}
			if connected {
				backoff = time.Second
			} else if backoff < maxEventBackoff {
				backoff *= 2
				if backoff > maxEventBackoff {
					backoff = maxEventBackoff
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
	}()
}

func pumpEvents(ctx context.Context, cfg *Config, onConnect func(bool), onEvent func(Notification)) bool {
	dialCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	conn, _, err := websocket.Dial(dialCtx, cfg.WSBase(), nil)
	cancel()
	if err != nil {
		return false
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	onConnect(true)
	defer onConnect(false)

	for {
		// 空闲超时当断线处理：aria2 不主动发 ping，长时间半开连接要能自愈。
		readCtx, stop := context.WithTimeout(ctx, eventIdleTTL)
		_, b, err := conn.Read(readCtx)
		stop()
		if err != nil {
			return true
		}
		var msg struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params []struct {
				GID string `json:"gid"`
			} `json:"params"`
		}
		if json.Unmarshal(b, &msg) != nil || msg.ID != "" || msg.Method == "" {
			continue
		}
		gid := ""
		if len(msg.Params) > 0 {
			gid = msg.Params[0].GID
		}
		onEvent(Notification{Method: msg.Method, GID: gid})
	}
}

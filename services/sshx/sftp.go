package sshx

// SFTP 文件操作的门面层。
//
// 这一层要做的三件事，一件都不能省：
//  1. 取凭据、取连接（复用连接池，避免第二个 TCP）
//  2. 把引擎层错误映射成稳定错误码
//  3. 保证 SFTP 子系统 channel 一定被关闭
//
// 每个方法都是「一次性」的：借连接 → 干活 → 还连接。
// 不做长驻句柄——文件操作是低密度的用户行为，为了它常驻一条 channel
// 会让服务端的 MaxSessions 先耗尽。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// EventSftpProgress 传输进度事件。
//
// 为什么用事件而不是返回值：SftpDownload/SftpUpload 是同步调用，大文件要传
// 几分钟，等返回值再更新界面等于全程静止——用户分不清「在传」和「卡死」。
// 引擎层 internal/sshx 早就留了 onProgress 回调，这里把它接到 Wails 事件上。
const EventSftpProgress = "quickdock:ssh:sftp-progress"

// sftpProgressInterval 进度事件的最小间隔。
//
// copyTrack 按 32KB 一个分片回调，一个 100MB 的文件会触发 3000 多次。
// 每次 Emit 都要跨到 JS 层并触发一次 Vue 渲染，不节流会把界面拖到掉帧。
// 100ms 对应的视觉刷新率已经够（人眼分辨不出更细），最后一片强制发送。
const sftpProgressInterval = 100 * time.Millisecond

// SftpProgressEvent 传给前端的一条进度。
//
// TransferID 由前端生成并随调用传入：并发传输（两个标签页各传一个）时
// 光靠 hostID 或文件名无法区分，前端只认自己发起的那一个。
type SftpProgressEvent struct {
	TransferID string `json:"transferId"`
	Op         string `json:"op"`   // upload | download
	Name       string `json:"name"` // 展示用的文件名
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
}

// sftpTimeout 拨号阶段的超时。
//
// 只管握手，不管传输：connPool.Acquire 拿到的 ctx 仅透传给 dial，
// 一旦 *ssh.Client 到手就不再受它约束。大文件来回传几百 MB 也不该被掐断——
// 真要限制的话得在 io.Copy 那一层做，现在没有这个需求，也不该假装有限制。
const sftpTimeout = 30 * time.Second

// SftpList 列举远端目录。path 为空时列的是远端登录家目录。
func (s *SshService) SftpList(hostID, remotePath string) *services.ApiResult {
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		res, err := sess.List(remotePath)
		return res, err
	})
}

// SftpStat 查询单个远端路径的属性。用于刷新/校验一次是否存在。
func (s *SshService) SftpStat(hostID, remotePath string) *services.ApiResult {
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return sess.Stat(remotePath)
	})
}

// SftpMkdir 创建目录（含缺失的中间层）。
func (s *SshService) SftpMkdir(hostID, remotePath string) *services.ApiResult {
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.Mkdir(remotePath)
	})
}

// SftpRemove 删除远端路径。recursive 为假且目标非空目录时报错。
func (s *SshService) SftpRemove(hostID, remotePath string, recursive bool) *services.ApiResult {
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.Remove(remotePath, recursive)
	})
}

// SftpRename 移动或重命名。
func (s *SshService) SftpRename(hostID, oldPath, newPath string) *services.ApiResult {
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.Rename(oldPath, newPath)
	})
}

// SftpReadText 读取文本文件前若干字节，用于快速查看。
func (s *SshService) SftpReadText(hostID, remotePath string, limit int64) *services.ApiResult {
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		text, err := sess.ReadText(remotePath, limit)
		return map[string]interface{}{"content": text}, err
	})
}

// SftpWriteText 覆盖写入文本文件。mode 传 0 表示不改动权限。
func (s *SshService) SftpWriteText(hostID, remotePath, content string, mode int) *services.ApiResult {
	if mode < 0 || mode > 0o7777 {
		return failCode(CodeInvalidInput, "文件权限取值无效")
	}
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.WriteText(remotePath, content, os.FileMode(mode))
	})
}

// SftpChmod 修改远端路径的权限位。mode 是八进制数值（如 0o644），不是字符串。
//
// 上限放到 0o7777 而不是 0o777：setuid/setgid/sticky 在部署脚本里是常用手段
// （比如让某个脚本可被普通用户以属主身份执行），前端也会显示成 4 位。
func (s *SshService) SftpChmod(hostID, remotePath string, mode int) *services.ApiResult {
	if mode < 0 || mode > 0o7777 {
		return failCode(CodeInvalidInput, "权限取值必须在 0000–7777 之间")
	}
	if strings.TrimSpace(remotePath) == "" {
		return failCode(CodeInvalidInput, "远端路径不能为空")
	}
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.Chmod(remotePath, os.FileMode(mode))
	})
}

// SftpDownload 下载远端文件或目录到本地。
//
// transferID 非空时按 sftpProgressInterval 节流推送进度事件，并登记进取消表
// 供 SftpCancelTransfer 中断；传空串表示调用方既不要进度也不要取消
// （脚本化调用、测试）。
func (s *SshService) SftpDownload(hostID, remotePath, localPath, transferID string) *services.ApiResult {
	local := strings.TrimSpace(localPath)
	if local == "" {
		return failCode(CodeInvalidInput, "本地路径不能为空")
	}
	ctx, done := s.trackTransfer(transferID)
	defer done()

	progress := s.progressEmitter(transferID, "download", path.Base(strings.TrimSuffix(remotePath, "/")))
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		if err := sess.Download(ctx, remotePath, local, progress); err != nil {
			return nil, wrapTransferErr(err, "下载失败")
		}
		return nil, nil
	})
}

// SftpUpload 上传本地文件或目录到远端目录。transferID 语义同 SftpDownload。
func (s *SshService) SftpUpload(hostID, localPath, remoteDir, transferID string) *services.ApiResult {
	local := strings.TrimSpace(localPath)
	if local == "" {
		return failCode(CodeInvalidInput, "本地路径不能为空")
	}
	ctx, done := s.trackTransfer(transferID)
	defer done()

	progress := s.progressEmitter(transferID, "upload", path.Base(strings.TrimSuffix(local, "/\\")))
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		if err := sess.Upload(ctx, local, remoteDir, progress); err != nil {
			return nil, wrapTransferErr(err, "上传失败")
		}
		return nil, nil
	})
}

// SftpCancelTransfer 取消一条进行中的传输。找不到对应 ID 时按成功返回：
// 传输可能在这次调用到达前就自己结束了，那不是错误——用户按取消，
// 结果文件已经传完，不该弹一条报错让他以为出了问题。
func (s *SshService) SftpCancelTransfer(transferID string) *services.ApiResult {
	id := strings.TrimSpace(transferID)
	if id == "" {
		return failCode(CodeInvalidInput, "缺少传输标识")
	}
	s.transferMu.Lock()
	cancel := s.transferCancels[id]
	s.transferMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return services.Ok(nil)
}

// trackTransfer 把一次传输登记进取消表，返回值里的 done 必须 defer 调用。
//
// 空 transferID 返回一个永不取消的 ctx：调用方没要进度就不要取消能力，
// 否则所有脚本化调用都得生成 ID 才能用。
func (s *SshService) trackTransfer(transferID string) (context.Context, func()) {
	id := strings.TrimSpace(transferID)
	if id == "" {
		return context.Background(), func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.transferMu.Lock()
	if s.transferCancels == nil {
		s.transferCancels = make(map[string]context.CancelFunc)
	}
	// 同 ID 覆盖：ID 由前端生成，正常情况下不会重复；真重复了说明调用方有 bug，
	// 这里覆盖而不是报错，至少不会让后一次传输无法取消。
	if old := s.transferCancels[id]; old != nil {
		old()
	}
	s.transferCancels[id] = cancel
	s.transferMu.Unlock()

	return ctx, func() {
		cancel()
		s.transferMu.Lock()
		delete(s.transferCancels, id)
		s.transferMu.Unlock()
	}
}

// wrapTransferErr 给传输错误补一句中文动词，但取消要原样返回。
//
// 引擎与操作系统的原始错误（"open ...: Access is denied." / "sftp: ..."）
// 几乎全是英文，直接甩给用户就是一行看不懂的报错；前缀说明「哪件事失败了」，
// 原始文本保留在后面供排查，%w 也保住错误链；而 cancelled 包上前缀后
// 服务层就认不出来了，前端会把它当失败弹红字。
func wrapTransferErr(err error, prefix string) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%s：%w", prefix, err)
}

// progressEmitter 生成带节流的进度回调。
//
// 返回 nil 是合法状态：引擎层的 onProgress 允许为空，等于不回调。
// 最后一片（done == total）不参与节流，否则进度条会停在 97% 不动——
// 传输结束但界面没收到终点，比没有进度条更让人怀疑。
func (s *SshService) progressEmitter(transferID, op, name string) func(done, total int64) {
	if strings.TrimSpace(transferID) == "" {
		return nil
	}
	var last time.Time
	return func(done, total int64) {
		now := time.Now()
		if done != total && now.Sub(last) < sftpProgressInterval {
			return
		}
		last = now
		s.emitSftpProgress(SftpProgressEvent{
			TransferID: transferID,
			Op:         op,
			Name:       name,
			Done:       done,
			Total:      total,
		})
	}
}

func (s *SshService) emitSftpProgress(ev SftpProgressEvent) {
	if s.App == nil {
		return
	}
	a := s.App.App()
	if a == nil {
		return
	}
	a.Event.Emit(EventSftpProgress, ev)
}

// withSftp 统一的「借连接 → 开 SFTP → 干活 → 收摊」脚手架。
//
// 之所以抽出来：这些方法里最容易写错的就是释放顺序。SFTP channel 必须
// 在归还连接引用之前关掉（虽然池不会因为先还引用就断连接，但 channel
// 是 limited resource，晚关等于多占一条），而连接引用又必须在任何路径
// 下都归还一次。写一份总比每个方法各自漏一次强。
func (s *SshService) withSftp(hostID string, fn func(*sshcore.SftpSession) (interface{}, error)) *services.ApiResult {
	host, secret, err := s.loadHost(hostID)
	if err != nil {
		return failSsh(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), sftpTimeout)
	defer cancel()

	client, release, err := s.pool.Acquire(ctx, host.ID, func(ctx context.Context) (*sshcore.Client, error) {
		return s.dial(ctx, host, secret)
	})
	if err != nil {
		return failSsh(err)
	}
	// 文件操作是短暂的，干完立刻归还引用。若此刻没有终端会话在用这条连接，
	// 引用归零会让池关闭 TCP——这是对的，用户只是传个文件的话，
	// 不该在后台留着一条常驻连接。
	defer release()

	sess, err := sshcore.NewSftpSession(client.SSH())
	if err != nil {
		return failSsh(err)
	}
	defer sess.Close()

	data, err := fn(sess)
	if err != nil {
		return failSsh(err)
	}
	if data == nil {
		return services.Ok(nil)
	}
	return services.Ok(data)
}

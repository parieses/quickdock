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
	"os"
	"strings"
	"time"

	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

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

// SftpDownload 下载远端文件或目录到本地。
func (s *SshService) SftpDownload(hostID, remotePath, localPath string) *services.ApiResult {
	local := strings.TrimSpace(localPath)
	if local == "" {
		return failCode(CodeInvalidInput, "本地路径不能为空")
	}
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.Download(remotePath, local, nil)
	})
}

// SftpUpload 上传本地文件到远端目录。
func (s *SshService) SftpUpload(hostID, localPath, remoteDir string) *services.ApiResult {
	local := strings.TrimSpace(localPath)
	if local == "" {
		return failCode(CodeInvalidInput, "本地路径不能为空")
	}
	return s.withSftp(hostID, func(sess *sshcore.SftpSession) (interface{}, error) {
		return nil, sess.Upload(local, remoteDir, nil)
	})
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

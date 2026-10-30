package sshx

import (
	"context"
	"database/sql"
	"errors"
	"net"

	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// 错误码。前端按 code 决定弹哪个对话框、显示哪类文案，不要匹配 msg 文本。
//
// 这是前后端之间唯一的稳定契约：msg 是给人看的中文，随时可能改。
const (
	CodeDBUnavailable        = "ssh_db_unavailable"
	CodeInvalidInput         = "ssh_invalid_input"
	CodeHostNotFound         = "ssh_host_not_found"
	CodeFolderNotConnectable = "ssh_folder_not_connectable"
	CodeNoCredential         = "ssh_no_credential"

	CodeHostKeyUnknown = "ssh_hostkey_unknown" // 首次连接，需展示指纹让用户确认
	CodeHostKeyChanged = "ssh_hostkey_changed" // 指纹变了，高危，必须强告警

	CodeAuthFailed           = "ssh_auth_failed"
	CodeUnreachable          = "ssh_unreachable"
	CodeAlgorithmUnsupported = "ssh_algorithm_unsupported"
	CodeNoAuthMethod         = "ssh_no_auth_method"
	CodeConnectionClosed     = "ssh_connection_closed"

	CodeExecTimeout  = "ssh_exec_timeout"
	CodeExecAborted  = "ssh_exec_aborted"
	CodeEmptyCommand = "ssh_empty_command"

	CodeSessionNotFound = "ssh_session_not_found"
	CodeInvalidPtySize  = "ssh_invalid_pty_size"

	// ---- SFTP 文件面板（#14）----
	CodeRemoteNotExist   = "ssh_remote_not_exist"
	CodeRemoteIsDir      = "ssh_remote_is_dir"
	CodeRemoteNotEmpty   = "ssh_remote_not_empty"
	CodeSftpSubsystemOff = "ssh_sftp_unavailable"
	// 用户主动取消了传输。前端据此不弹错误：取消是正常操作，
	// 把它渲染成红色失败提示，等于让用户以为自己把什么东西弄坏了。
	CodeTransferCanceled = "ssh_transfer_canceled"

	// 用户取消了批量执行。与 CodeTransferCanceled 分开：
	// 两者在网上取消的是完全不同的东西，结果表中一行「传输已取消」
	// 会让人去找哪个文件传了一半，而实际上根本没传过文件。
	CodeBatchCanceled = "ssh_batch_canceled"

	CodeNoPendingKey        = "ssh_no_pending_key"
	CodeFingerprintMismatch = "ssh_fingerprint_mismatch"
	CodeInternal            = "ssh_internal"
)

// 门面层自身的错误。
var (
	errDBUnavailable        = errors.New("数据库未就绪")
	errInvalidInput         = errors.New("参数无效")
	errFolderNotConnectable = errors.New("文件夹不能建立连接，请选择具体主机")
	errNoCredential         = errors.New("该主机没有可用的登录凭据，请先填写密码或私钥")
)

// failSsh 把错误转成带错误码的 ApiResult（同时按仓库惯例记一条错误日志）。
func failSsh(err error) *services.ApiResult {
	return failSshWith(err, nil)
}

// failSshWith 在错误码之外追加业务数据（如超时场景要带回已收到的输出）。
func failSshWith(err error, extra map[string]interface{}) *services.ApiResult {
	r := services.Fail(err)
	code, detail := classifySshError(err)
	data := make(map[string]interface{}, len(detail)+len(extra)+1)
	data["errorCode"] = code
	for k, v := range detail {
		data[k] = v
	}
	for k, v := range extra {
		data[k] = v
	}
	r.Data = data
	return r
}

// failCode 用于「已经知道错误码」的场景（如 DB 未就绪，CheckDB 已记过日志）。
func failCode(code, msg string) *services.ApiResult {
	r := services.FailMsg(msg)
	r.Data = map[string]interface{}{"errorCode": code}
	return r
}

// classifySshError 把引擎/存储错误收敛成稳定的错误码。
//
// 顺序有讲究：带类型的错误（指纹两类）必须最先判断——它们同时 Unwrap 到
// 对应的 Sentinel，放在后面就会被更宽的分类抢走，前端也就拿不到新旧指纹了。
func classifySshError(err error) (string, map[string]interface{}) {
	var unknown *sshcore.UnknownHostKeyError
	if errors.As(err, &unknown) {
		return CodeHostKeyUnknown, map[string]interface{}{
			"host":        unknown.Host,
			"port":        unknown.Port,
			"keyType":     unknown.KeyType,
			"fingerprint": unknown.Fingerprint,
			"knownTypes":  unknown.KnownTypes,
		}
	}

	var changed *sshcore.HostKeyChangedError
	if errors.As(err, &changed) {
		return CodeHostKeyChanged, map[string]interface{}{
			"host":           changed.Host,
			"port":           changed.Port,
			"keyType":        changed.KeyType,
			"oldFingerprint": changed.OldFingerprint,
			"newFingerprint": changed.NewFingerprint,
		}
	}

	switch {
	case errors.Is(err, errDBUnavailable):
		return CodeDBUnavailable, nil
	case errors.Is(err, errInvalidInput):
		return CodeInvalidInput, nil
	case errors.Is(err, db.ErrSshValidation):
		// 用户填错了表单（主机地址为空、父节点不是文件夹、移成环……）。
		// 必须归到「参数无效」而不是兜底的 internal，否则前端会显示「内部错误」，
		// 用户完全不知道是自己漏填了一个字段。
		return CodeInvalidInput, nil
	case errors.Is(err, errFolderNotConnectable):
		return CodeFolderNotConnectable, nil
	case errors.Is(err, errNoCredential):
		return CodeNoCredential, nil
	case errors.Is(err, sql.ErrNoRows):
		return CodeHostNotFound, nil

	case errors.Is(err, sshcore.ErrAuthFailed):
		return CodeAuthFailed, nil
	case errors.Is(err, sshcore.ErrUnreachable):
		return CodeUnreachable, nil
	case errors.Is(err, sshcore.ErrAlgorithmGone):
		return CodeAlgorithmUnsupported, nil
	case errors.Is(err, sshcore.ErrNoAuthMethod):
		return CodeNoAuthMethod, nil
	case errors.Is(err, sshcore.ErrClosed):
		return CodeConnectionClosed, nil

	case errors.Is(err, sshcore.ErrExecTimeout):
		return CodeExecTimeout, nil
	case errors.Is(err, sshcore.ErrExecAborted):
		return CodeExecAborted, nil
	case errors.Is(err, sshcore.ErrEmptyCommand):
		return CodeEmptyCommand, nil

	case errors.Is(err, sshcore.ErrSessionNotFound):
		return CodeSessionNotFound, nil
	case errors.Is(err, sshcore.ErrInvalidPtySize):
		return CodeInvalidPtySize, nil

	case errors.Is(err, sshcore.ErrNoPendingHostKey):
		return CodeNoPendingKey, nil
	case errors.Is(err, sshcore.ErrFingerprintMismatch):
		return CodeFingerprintMismatch, nil
	case errors.Is(err, sshcore.ErrKnownHostStoreMissing):
		return CodeDBUnavailable, nil

	// SFTP：这几个哨兵必须排在 net.Error 之前。它们的 Unwrap 链里夹着
	// SSH 层的传输错误，掉到下面会被误判成「主机连不上」，
	// 用户看到的就是「服务器不可达」——但真正的问题是文件不存在或没权限。
	case errors.Is(err, sshcore.ErrRemoteNotExist):
		return CodeRemoteNotExist, nil
	case errors.Is(err, sshcore.ErrRemoteIsDir):
		return CodeRemoteIsDir, nil
	case errors.Is(err, sshcore.ErrRemoteNotEmpty):
		return CodeRemoteNotEmpty, nil
	case errors.Is(err, sshcore.ErrSftpUnavailable):
		return CodeSftpSubsystemOff, nil
	case errors.Is(err, sshcore.ErrInvalidRemotePath):
		return CodeInvalidInput, nil
	// 传输被用户取消。必须排在网络的兜底判断之前——context.Canceled 在
	// Go 1.20+ 也满足 net.Error 的 Timeout() 契约（返回 false），
	// 掉到下面会被判成「主机连不上」，用户明明是自己按的取消。
	case errors.Is(err, context.Canceled):
		return CodeTransferCanceled, nil
	}

	// 兜底：剩下的多半是拨号阶段的网络错误（拒绝连接、DNS 失败、超时）。
	var netErr net.Error
	if errors.As(err, &netErr) {
		return CodeUnreachable, map[string]interface{}{"timeout": netErr.Timeout()}
	}
	return CodeInternal, nil
}

package sshx

import (
	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// knownHostStore 把 db 层的已信任密钥表适配成引擎要的 sshx.HostKeyStore。
//
// 为什么要这一层适配，而不是让 db 直接实现 sshx 的接口：
// db 是最底层，反向 import SSH 引擎会让引擎类型变更波及数据库层；
// 适配只有三个方法，放在门面层成本最低。两边记录类型也刻意保持字段同构，
// 所以转换是纯映射、没有逻辑。
type knownHostStore struct {
	app *services.AppService
}

func (k knownHostStore) ListKnownHosts(host string, port int) ([]sshcore.KnownHost, error) {
	// 每次调用重新读 app.DB：DB 在 ServiceStartup 阶段才注入，
	// 而本适配器在 main 里构造得更早，缓存指针会拿到 nil。
	if k.app.DB == nil {
		return nil, errDBUnavailable
	}
	recs, err := k.app.DB.ListSshKnownHosts(host, port)
	if err != nil {
		return nil, err
	}
	out := make([]sshcore.KnownHost, 0, len(recs))
	for _, r := range recs {
		out = append(out, sshcore.KnownHost{
			Host:        r.Host,
			Port:        r.Port,
			KeyType:     r.KeyType,
			Fingerprint: r.Fingerprint,
			AddedAt:     r.AddedAt,
		})
	}
	return out, nil
}

func (k knownHostStore) SaveKnownHost(kh sshcore.KnownHost) error {
	if k.app.DB == nil {
		return errDBUnavailable
	}
	return k.app.DB.SaveSshKnownHost(db.SshKnownHost{
		Host:        kh.Host,
		Port:        kh.Port,
		KeyType:     kh.KeyType,
		Fingerprint: kh.Fingerprint,
		AddedAt:     kh.AddedAt,
	})
}

func (k knownHostStore) DeleteKnownHosts(host string, port int) error {
	if k.app.DB == nil {
		return errDBUnavailable
	}
	return k.app.DB.DeleteSshKnownHosts(host, port)
}

package app

import (
	migrations "github.com/nickZFZ/Sydom/db/migrations"
	"github.com/nickZFZ/Sydom/internal/db"
)

// runMigrate 是 -migrate 模式主体：轻量取 DSN → 应用嵌入的迁移（幂等 up）→ 返回。
// 不装载密钥/TLS、不起监听器（迁移无关运行时凭据）。
func runMigrate(path string, getenv func(string) string) error {
	dsn, err := LoadDSN(path, getenv)
	if err != nil {
		return err
	}
	return db.RunMigrationsFS(dsn, migrations.FS)
}

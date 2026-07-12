package db

import (
	"errors"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// RunMigrations 对 dsn 指向的数据库应用 sourceURL 下的全部 up migration。
// sourceURL 形如 "file://../../db/migrations"，dsn 形如 "postgres://...".
func RunMigrations(dsn, sourceURL string) error {
	m, err := migrate.New(sourceURL, dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// RunMigrationsFS 对 dsn 指向的数据库应用 fsys 里的全部 up migration（幂等）。
// fsys 通常是 //go:embed 的 embed.FS（生产路径，迁移编入二进制、与代码同版本）。
func RunMigrationsFS(dsn string, fsys fs.FS) error {
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// MigrateDown 回滚全部 migration（供 up/down 往返测试使用）。
func MigrateDown(dsn, sourceURL string) error {
	m, err := migrate.New(sourceURL, dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

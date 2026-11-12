package database

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"wecomrelay/internal/config"
	"wecomrelay/internal/model"
)

// Connect 连接 MySQL，并在容器启动初期按固定间隔重试。
func Connect(cfg config.DBConfig, debug bool) (*gorm.DB, error) {
	logMode := logger.Warn
	if debug {
		logMode = logger.Info
	}
	// 查询不到记录属于正常业务分支（如空死信列表、登录用户不存在），不打印为错误。
	gormLogger := logger.New(
		log.New(os.Stdout, "", log.LstdFlags),
		logger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  logMode,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	var db *gorm.DB
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		db, err = gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
			Logger:                                   gormLogger,
			DisableForeignKeyConstraintWhenMigrating: true,
		})
		if err == nil {
			sqlDB, pingErr := db.DB()
			if pingErr == nil {
				pingErr = sqlDB.Ping()
			}
			if pingErr == nil {
				sqlDB.SetMaxOpenConns(50)
				sqlDB.SetMaxIdleConns(10)
				sqlDB.SetConnMaxLifetime(time.Hour)
				return db, nil
			}
			err = pingErr
		}
		slog.Warn("等待数据库就绪", "attempt", attempt, "error", err)
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("连接数据库失败: %w", err)
}

// Migrate 自动创建/更新业务表。
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.AdminUser{},
		&model.Consumer{},
		&model.WecomConfig{},
		&model.RelayTask{},
		&model.DeadLetter{},
		&model.AuditLog{},
	)
}

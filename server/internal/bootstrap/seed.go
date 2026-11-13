package bootstrap

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"wecomrelay/internal/config"
	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
)

// SeedAdmin 首次启动时创建初始管理员。
func SeedAdmin(db *gorm.DB, cfg *config.Config, logger *slog.Logger) error {
	var count int64
	if err := db.Model(&model.AdminUser{}).Count(&count).Error; err != nil {
		return fmt.Errorf("查询管理员失败: %w", err)
	}
	if count > 0 {
		return nil
	}
	if cfg.AdminPassword == "" {
		return fmt.Errorf("首次启动需设置 ADMIN_PASSWORD（至少 8 个字符）")
	}
	if len(cfg.AdminPassword) < 8 {
		return fmt.Errorf("ADMIN_PASSWORD 至少需要 8 个字符")
	}
	hash, err := security.HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("管理员密码加密失败: %w", err)
	}
	admin := model.AdminUser{
		Username:     cfg.AdminUsername,
		PasswordHash: hash,
		Nickname:     "超级管理员",
		Role:         model.RoleAdmin,
		Enabled:      true,
		TokenVersion: 1,
	}
	if err := db.Create(&admin).Error; err != nil {
		return fmt.Errorf("创建初始管理员失败: %w", err)
	}
	logger.Info("已创建初始管理员", "username", cfg.AdminUsername)
	return nil
}

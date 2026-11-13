package api

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"wecomrelay/internal/model"
)

// parseID 解析路径中的无符号整数 ID。
func parseID(c *gin.Context, name string) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		Fail(c, 400, "ID 不合法")
		return 0, false
	}
	return id, true
}

// validCallbackURL 校验回调地址必须为 http/https。
func validCallbackURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// isMaskedOrEmpty 判断敏感字段是否表示“保持不变”。
func isMaskedOrEmpty(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || v == "******"
}

// boolQuery 解析可选布尔查询参数。
func boolQuery(c *gin.Context, key string) (bool, bool) {
	raw, ok := c.GetQuery(key)
	if !ok {
		return false, false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return v, true
}

// bumpTokenVersion 递增管理员令牌版本，使该账号已签发的 JWT 立即失效。
func bumpTokenVersion(db *gorm.DB, userID uint64) error {
	if userID == 0 {
		return gorm.ErrRecordNotFound
	}
	result := db.Model(&model.AdminUser{}).
		Where("id = ?", userID).
		UpdateColumn("token_version", gorm.Expr("token_version + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

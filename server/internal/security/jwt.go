package security

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"wecomrelay/internal/model"
)

// Claims 管理端 JWT 载荷。
type Claims struct {
	UserID       uint64 `json:"uid"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	TokenVersion int64  `json:"tv"`
	jwt.RegisteredClaims
}

// Manager 负责签发与校验 JWT。
type Manager struct {
	secret []byte
	expire time.Duration
}

func NewJWTManager(secret string, expire time.Duration) *Manager {
	return &Manager{secret: []byte(secret), expire: expire}
}

// Generate 为管理员签发令牌。
func (m *Manager) Generate(user model.AdminUser) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(m.expire)
	claims := Claims{
		UserID:       user.ID,
		Username:     user.Username,
		Role:         user.Role,
		TokenVersion: user.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "wecom-callback-relay",
			Subject:   user.Username,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("签发令牌失败: %w", err)
	}
	return signed, expiresAt, nil
}

// Parse 校验并解析令牌。
func (m *Manager) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("不支持的签名算法")
		}
		return m.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return nil, errors.New("令牌无效或已过期")
	}
	return claims, nil
}

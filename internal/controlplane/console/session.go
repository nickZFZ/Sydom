// Package console 是控制面 Admin Web Console（服务端 BFF）：把 AdminService 包成
// html/template 渲染的人面管理界面，复用 mgmt.AuthorizeRule/CheckStatusWrite 鉴权核心。
package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNoSession 表示会话不存在/已过期（fail-close：调用方一律重定向登录）。
var ErrNoSession = errors.New("console: no session")

// Session 是会话状态。绝不含 operator secret。
type Session struct {
	Principal string `json:"principal"`
	CSRF      string `json:"csrf"`
	CreatedAt int64  `json:"created_at"`
}

// RedisStore 以 Redis 为后端的会话存储，键 console:sess:<id>，带空闲 TTL。
type RedisStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewRedisStore(rdb *redis.Client, ttl time.Duration) *RedisStore {
	return &RedisStore{rdb: rdb, ttl: ttl}
}

func (s *RedisStore) key(id string) string { return "console:sess:" + id }

// randToken 返回 32 字节随机的 base64url 串（session ID / CSRF token 共用）。
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create 新建会话，返回 (sessionID, csrfToken)。
func (s *RedisStore) Create(ctx context.Context, principal string) (string, string, error) {
	id, err := randToken()
	if err != nil {
		return "", "", err
	}
	csrf, err := randToken()
	if err != nil {
		return "", "", err
	}
	sess := Session{Principal: principal, CSRF: csrf, CreatedAt: time.Now().Unix()}
	raw, err := json.Marshal(sess)
	if err != nil {
		return "", "", err
	}
	if err := s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err(); err != nil {
		return "", "", err
	}
	return id, csrf, nil
}

// Get 取会话；命中则续期空闲 TTL。未命中返回 ErrNoSession。
func (s *RedisStore) Get(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return Session{}, ErrNoSession
	}
	raw, err := s.rdb.Get(ctx, s.key(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return Session{}, err
	}
	_ = s.rdb.Expire(ctx, s.key(id), s.ttl).Err() // 续期，失败不致命
	return sess, nil
}

func (s *RedisStore) Delete(ctx context.Context, id string) error {
	return s.rdb.Del(ctx, s.key(id)).Err()
}

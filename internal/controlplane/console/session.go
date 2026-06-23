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
	Flash     string `json:"flash,omitempty"` // 一次性成功提示（业务语言，绝不含 secret）
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
	if id == "" {
		return nil // 空 id 无会话可删，幂等返回（与 Get 空 id 短路对称）
	}
	return s.rdb.Del(ctx, s.key(id)).Err()
}

// SetFlash 给已存在会话写一条一次性 flash（读-改-写，保留 TTL 用 s.ttl 续期）。
// 非原子（读-改-写）：Beta 低频写场景可接受，同一会话并发写 flash 概率极低。
func (s *RedisStore) SetFlash(ctx context.Context, id, msg string) error {
	if id == "" {
		return ErrNoSession
	}
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	sess.Flash = msg
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err()
}

// TakeFlash 取并清空 flash（一次性）。无 flash 返回 ""。
// 非原子（读-改-写）：Beta 低频场景可接受，同一会话并发读 flash 概率极低。
func (s *RedisStore) TakeFlash(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	sess, err := s.Get(ctx, id)
	if errors.Is(err, ErrNoSession) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if sess.Flash == "" {
		return "", nil
	}
	msg := sess.Flash
	sess.Flash = ""
	raw, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err(); err != nil {
		return "", err
	}
	return msg, nil
}

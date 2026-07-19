package console

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// oidcState 是发起→回调间的一时态（10min TTL，回调 GETDEL 一次性）。绝不含 secret/issuer。
type oidcState struct {
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	TenantID int64  `json:"tenant_id"`
	ReturnTo string `json:"return_to"`
}

func (s *RedisStore) oidcStateKey(state string) string { return "console:oidcstate:" + state }

// PutOIDCState 写一时态。
func (s *RedisStore) PutOIDCState(ctx context.Context, state string, v oidcState, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.oidcStateKey(state), raw, ttl).Err()
}

// TakeOIDCState 原子 GETDEL：命中即删（一次性，防重放）。未命中→ok=false。
func (s *RedisStore) TakeOIDCState(ctx context.Context, state string) (oidcState, bool, error) {
	if state == "" {
		return oidcState{}, false, nil
	}
	raw, err := s.rdb.GetDel(ctx, s.oidcStateKey(state)).Bytes()
	if errors.Is(err, redis.Nil) {
		return oidcState{}, false, nil
	}
	if err != nil {
		return oidcState{}, false, err
	}
	var v oidcState
	if err := json.Unmarshal(raw, &v); err != nil {
		return oidcState{}, false, err
	}
	return v, true, nil
}

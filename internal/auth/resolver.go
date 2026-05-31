package auth

import "context"

// SecretResolver 按 app_id 返回其 AppSecret 原文
// （控制面从 application.app_secret_enc 解密得到）。
// 本子项目只定义接口；DB 解密实现归控制面 spec。
type SecretResolver interface {
	ResolveSecret(ctx context.Context, appID string) (secret []byte, err error)
}

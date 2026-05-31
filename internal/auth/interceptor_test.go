package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, appID string) ([]byte, error) {
	s, ok := f[appID]
	if !ok {
		return nil, errors.New("app not found")
	}
	return s, nil
}

const testMethod = "/sydom.sync.v1.PolicySync/PullSnapshot"

func mdCtx(appID string, ts int64, sig string) context.Context {
	md := metadata.New(map[string]string{
		MDAppID:     appID,
		MDTimestamp: strconv.FormatInt(ts, 10),
		MDSignature: sig,
	})
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestAuthenticate_Success(t *testing.T) {
	secret := []byte("s3cr3t")
	res := fakeResolver{"AK_order": secret}
	now := time.Unix(1700000000, 0)
	sig := Sign(secret, "AK_order", now.Unix(), testMethod)

	ctx, err := authenticate(mdCtx("AK_order", now.Unix(), sig), res, testMethod, now)
	require.NoError(t, err)
	id, ok := AppIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "AK_order", id)
}

func TestAuthenticate_MissingMetadata(t *testing.T) {
	res := fakeResolver{}
	_, err := authenticate(context.Background(), res, testMethod, time.Now())
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticate_StaleTimestamp(t *testing.T) {
	secret := []byte("s3cr3t")
	res := fakeResolver{"AK_order": secret}
	signedAt := time.Unix(1700000000, 0)
	sig := Sign(secret, "AK_order", signedAt.Unix(), testMethod)

	now := signedAt.Add(10 * time.Minute) // 超出 ±5 分钟窗口
	_, err := authenticate(mdCtx("AK_order", signedAt.Unix(), sig), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticate_BadSignature(t *testing.T) {
	res := fakeResolver{"AK_order": []byte("s3cr3t")}
	now := time.Unix(1700000000, 0)
	_, err := authenticate(mdCtx("AK_order", now.Unix(), "deadbeef"), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestAuthenticate_UnknownApp(t *testing.T) {
	res := fakeResolver{}
	now := time.Unix(1700000000, 0)
	sig := Sign([]byte("x"), "AK_ghost", now.Unix(), testMethod)
	_, err := authenticate(mdCtx("AK_ghost", now.Unix(), sig), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// 承接任务 4 代码审查 P1/P5：appID 来自不可信 metadata，含控制字符/换行的 appID 必须被拒。
func TestAuthenticate_RejectsMalformedAppID(t *testing.T) {
	secret := []byte("s3cr3t")
	bad := "AK\norder" // 含换行
	res := fakeResolver{bad: secret}
	now := time.Unix(1700000000, 0)
	sig := Sign(secret, bad, now.Unix(), testMethod)
	_, err := authenticate(mdCtx(bad, now.Unix(), sig), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// 任务 5 代码审查 P2：resolver 返回空 secret 时必须 fail-close（空密钥 HMAC 人人可算）。
func TestAuthenticate_RejectsEmptySecret(t *testing.T) {
	res := fakeResolver{"AK_order": []byte{}} // 空密钥（模拟 DB 损坏/解密失败）
	now := time.Unix(1700000000, 0)
	sig := Sign([]byte{}, "AK_order", now.Unix(), testMethod)
	_, err := authenticate(mdCtx("AK_order", now.Unix(), sig), res, testMethod, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// 任务 5 代码审查 P4：validAppID 限定 ASCII 可打印非空格，挡控制字符/空格/Unicode 同形字。
func TestValidAppID(t *testing.T) {
	require.True(t, validAppID("AK_order"))
	require.True(t, validAppID("AK-order.v2"))
	require.False(t, validAppID(""))              // 空（注：authenticate 另有非空前置检查）
	require.False(t, validAppID("AK order"))      // 空格
	require.False(t, validAppID("AK\norder"))     // 换行
	require.False(t, validAppID("AK\torder"))     // 制表符
	require.False(t, validAppID("AK\u200border")) // 零宽空格（Unicode 混淆）
	require.False(t, validAppID("订单"))            // 非 ASCII
}

func TestMethodFromURI(t *testing.T) {
	// 带 scheme/authority 的完整 URI 取路径部分
	require.Equal(t, "/sydom.sync.v1.PolicySync/PullSnapshot",
		methodFromURI("https://authority/sydom.sync.v1.PolicySync/PullSnapshot"))
	// 无 scheme 时原样返回（兜底；与服务端 FullMethod 不匹配将 fail-close 拒绝）
	require.Equal(t, "bufnet", methodFromURI("bufnet"))
}

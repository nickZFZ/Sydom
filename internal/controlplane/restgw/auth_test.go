package restgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, p string) ([]byte, error) {
	s, ok := f[p]
	if !ok {
		return nil, errors.New("unknown")
	}
	return s, nil
}

// signedReqTS 构造一个对 (principal, ts, method, target, sha256(body)) 正确签名的 httptest 请求。
func signedReqTS(t *testing.T, secret []byte, principal, method, target string, body []byte, ts int64) *http.Request {
	t.Helper()
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set(auth.HdrPrincipal, principal)
	req.Header.Set(auth.HdrTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(auth.HdrSignature, auth.SignREST(secret, principal, ts, method, req.URL.RequestURI(), h))
	return req
}

func TestAuthenticateHTTP_Success(t *testing.T) {
	secret := []byte("s3cr3t")
	res := fakeResolver{"root": secret}
	now := time.Unix(1700000000, 0)
	req := signedReqTS(t, secret, "root", "GET", "/v1/applications", nil, now.Unix())

	p, err := authenticateHTTP(req, nil, res, now)
	require.NoError(t, err)
	require.Equal(t, "root", p)
}

func TestAuthenticateHTTP_Failures(t *testing.T) {
	secret := []byte("s3cr3t")
	res := fakeResolver{"root": secret}
	now := time.Unix(1700000000, 0)
	body := []byte(`{"code":"x"}`)

	// 缺头部 → 401。
	bare := httptest.NewRequest("POST", "/v1/apps/5/roles", nil)
	_, err := authenticateHTTP(bare, body, res, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 非法 principal → 401。
	bad := signedReqTS(t, secret, "ro ot", "GET", "/v1/applications", nil, now.Unix())
	_, err = authenticateHTTP(bad, nil, res, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 时间偏移越界 → 401。
	stale := signedReqTS(t, secret, "root", "GET", "/v1/applications", nil, now.Add(-10*time.Minute).Unix())
	_, err = authenticateHTTP(stale, nil, res, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 坏签名 → 401。
	tampered := signedReqTS(t, secret, "root", "GET", "/v1/applications", nil, now.Unix())
	tampered.Header.Set(auth.HdrSignature, "deadbeef")
	_, err = authenticateHTTP(tampered, nil, res, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 未知 operator（resolver error）→ 401 通用（不泄露存在性）。
	ghost := signedReqTS(t, secret, "ghost", "GET", "/v1/applications", nil, now.Unix())
	_, err = authenticateHTTP(ghost, nil, res, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 空密钥 fail-close → 401。
	emptyRes := fakeResolver{"root": {}}
	er := signedReqTS(t, []byte{}, "root", "GET", "/v1/applications", nil, now.Unix())
	_, err = authenticateHTTP(er, nil, emptyRes, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 改 body（签名按空 body 算，实际 body 不空）→ 401。
	wrongBody := signedReqTS(t, secret, "root", "POST", "/v1/apps/5/roles", nil, now.Unix())
	_, err = authenticateHTTP(wrongBody, body, res, now)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

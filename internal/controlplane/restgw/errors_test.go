package restgw

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHTTPStatusForCode(t *testing.T) {
	cases := map[codes.Code]int{
		codes.OK:                 http.StatusOK,
		codes.InvalidArgument:    http.StatusBadRequest,
		codes.Unauthenticated:    http.StatusUnauthorized,
		codes.PermissionDenied:   http.StatusForbidden,
		codes.NotFound:           http.StatusNotFound,
		codes.AlreadyExists:      http.StatusConflict,
		codes.FailedPrecondition: http.StatusConflict,
		codes.ResourceExhausted:  http.StatusTooManyRequests, // 配额超限（M6.1a）
		codes.Unavailable:        http.StatusServiceUnavailable,
		codes.Internal:           http.StatusInternalServerError,
		codes.Unknown:            http.StatusInternalServerError,
		codes.DataLoss:           http.StatusInternalServerError, // 兜底
	}
	for c, want := range cases {
		require.Equal(t, want, httpStatusForCode(c), c.String())
	}
}

func TestWriteError_ScrubsInternalDetail(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	// 模拟 mgmt 把内部细节塞进 Internal message。
	err := status.Error(codes.Internal, "write: pq: duplicate key value violates unique constraint \"secret_leak\"")
	writeError(rec, logger, "root", "/sydom.admin.v1.AdminService/CreateRole", err)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body struct{ Code, Message string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "internal", body.Code)
	require.Equal(t, "internal error", body.Message)         // 通用文案
	require.NotContains(t, rec.Body.String(), "secret_leak") // 内部细节绝不外泄
	require.NotContains(t, rec.Body.String(), "constraint")
}

func TestWriteError_PassesThroughSafeMessage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	err := status.Error(codes.InvalidArgument, "invalid effect")
	writeError(rec, logger, "root", "/m", err)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var body struct{ Code, Message string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "invalid_argument", body.Code)
	require.Equal(t, "invalid effect", body.Message) // 非 Internal：安全文案透传
	require.False(t, strings.Contains(body.Code, " "))
}

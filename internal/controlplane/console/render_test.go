package console

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testLogger(t *testing.T) *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHTTPStatusForCode(t *testing.T) {
	require.Equal(t, 403, httpStatusForCode(codes.PermissionDenied))
	require.Equal(t, 404, httpStatusForCode(codes.NotFound))
	require.Equal(t, 409, httpStatusForCode(codes.FailedPrecondition))
	require.Equal(t, 400, httpStatusForCode(codes.InvalidArgument))
	require.Equal(t, 500, httpStatusForCode(codes.Internal))
}

func TestRenderError_InternalScrubbed(t *testing.T) {
	h := &Handler{logger: testLogger(t), templates: mustTemplates()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	err := status.Error(codes.Internal, "constraint admin_operator_pkey violated")
	h.renderGRPCError(w, r, "/sydom.admin.v1.AdminService/CreateOperator", err)
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "admin_operator_pkey", "Internal 细节绝不外泄")
	require.Contains(t, w.Body.String(), "internal")
}

func TestRenderError_PermissionDenied(t *testing.T) {
	h := &Handler{logger: testLogger(t), templates: mustTemplates()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	h.renderGRPCError(w, r, "m", status.Error(codes.PermissionDenied, "permission denied"))
	require.Equal(t, 403, w.Code)
}

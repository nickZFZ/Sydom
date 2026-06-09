// Package restgw 把控制面 AdminService（gRPC）映射为对外程序化 REST/JSON 接口：
// 一张静态路由表 + 一条固定中间件管线（认证→鉴权→直调 service→编码）。
package restgw

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// httpStatusForCode 把 gRPC code 映射为 HTTP status（其余一律 500）。
func httpStatusForCode(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.FailedPrecondition:
		return http.StatusConflict
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default: // Internal / Unknown / DataLoss / ...
		return http.StatusInternalServerError
	}
}

// errBody 是对外错误响应体。
type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// codeName 把 gRPC code 映射为 snake_case 串。
// （grpc-go 的 codes.Code.String() 返回 CamelCase 如 "InvalidArgument"，不是 snake，故自建映射。）
func codeName(c codes.Code) string {
	switch c {
	case codes.OK:
		return "ok"
	case codes.InvalidArgument:
		return "invalid_argument"
	case codes.Unauthenticated:
		return "unauthenticated"
	case codes.PermissionDenied:
		return "permission_denied"
	case codes.NotFound:
		return "not_found"
	case codes.AlreadyExists:
		return "already_exists"
	case codes.FailedPrecondition:
		return "failed_precondition"
	case codes.Unavailable:
		return "unavailable"
	case codes.Internal:
		return "internal"
	default: // Unknown / DataLoss / ...
		return "internal"
	}
}

// writeError 把 gRPC status 错误映射为 HTTP status + JSON body。
// 安全铁律：Internal/Unknown（→500）一律回通用文案，原始细节只进服务端日志（带 principal/method），
// 防 PolicyManager 内部细节（约束名/SQL）经 REST 外泄。
func writeError(w http.ResponseWriter, logger *slog.Logger, principal, method string, err error) {
	st := status.Convert(err)
	httpStatus := httpStatusForCode(st.Code())

	msg := st.Message()
	if httpStatus == http.StatusInternalServerError {
		logger.Error("restgw internal error",
			"principal", principal, "method", method, "code", st.Code().String(), "detail", st.Message())
		msg = "internal error"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(errBody{Code: codeName(st.Code()), Message: msg})
}

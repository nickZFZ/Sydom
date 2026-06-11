package console

import (
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pathUint64 从 path 段解析 uint64（path 权威）。失败 → InvalidArgument。
func pathUint64(r *http.Request, key string) (uint64, error) {
	v, err := strconv.ParseUint(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "无效的 %s", key)
	}
	return v, nil
}

// pathInt64 从 path 段解析 int64（path 权威）。失败 → InvalidArgument。
func pathInt64(r *http.Request, key string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "无效的 %s", key)
	}
	return v, nil
}

// formInt64 从表单字段解析 int64；空值返回 0（可选字段）。非空且无效 → InvalidArgument。
func formInt64(r *http.Request, key string) (int64, error) {
	s := r.FormValue(key)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "无效的 %s", key)
	}
	return v, nil
}

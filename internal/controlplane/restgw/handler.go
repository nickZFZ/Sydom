package restgw

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// maxBodyBytes 限请求体 1 MiB，防大 body DoS（安全铁律）。
const maxBodyBytes = 1 << 20

// Handler 持有 REST 网关依赖（全部注入，与 gRPC 端共用同一实例）。
type Handler struct {
	srv      *mgmt.AdminServer
	resolver auth.SecretResolver
	enf      *adminauthz.Enforcer
	db       *sql.DB
	logger   *slog.Logger
}

// NewHandler 装配 ServeMux：每条路由注册方法感知模式，绑定统一中间件管线。
func NewHandler(srv *mgmt.AdminServer, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db *sql.DB, logger *slog.Logger) http.Handler {
	h := &Handler{srv: srv, resolver: resolver, enf: enf, db: db, logger: logger}
	mux := http.NewServeMux()
	for _, rt := range allRoutes() {
		mux.HandleFunc(rt.method+" "+rt.pattern, h.serve(rt))
	}
	return mux
}

// serve 返回一条路由的中间件管线：读 body → 认证 → 解码 → 授权 → status 闸 → 直调 → 编码。
func (h *Handler) serve(rt route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 读 body（上限 1 MiB；超限 → 400）。
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, h.logger, r.Header.Get(auth.HdrPrincipal), rt.fullMethod,
				status.Error(codes.InvalidArgument, "request body too large"))
			return
		}
		// 2. REST-HMAC 认证。
		principal, err := authenticateHTTP(r, body, h.resolver, time.Now())
		if err != nil {
			writeError(w, h.logger, r.Header.Get(auth.HdrPrincipal), rt.fullMethod, err)
			return
		}
		// 3. 解码 path/query/body → proto（path 权威覆写）。
		msg, err := rt.decode(r, body)
		if err != nil {
			writeError(w, h.logger, principal, rt.fullMethod, err)
			return
		}
		// 4. 共享授权核心（system→"*"，否则 path app_id）。
		ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, rt.fullMethod, principal, msg)
		if err != nil {
			writeError(w, h.logger, principal, rt.fullMethod, err)
			return
		}
		// 5. status 写闸（必在 authz 之后，否则泄露 app 存在性）。
		if err := mgmt.CheckStatusWrite(ctx, h.db, rt.fullMethod, msg); err != nil {
			writeError(w, h.logger, principal, rt.fullMethod, err)
			return
		}
		// 6. 直调 *AdminServer 方法（零网络跳，ctx 携 operator）。
		resp, err := rt.invoke(ctx, h.srv, msg)
		if err != nil {
			writeError(w, h.logger, principal, rt.fullMethod, err)
			return
		}
		// 7. canonical protojson 编码。
		h.writeJSON(w, principal, rt.fullMethod, resp)
	}
}

// writeJSON 以 canonical protojson 编码响应（lowerCamelCase、uint64-as-string、默认值也输出）。
func (h *Handler) writeJSON(w http.ResponseWriter, principal, method string, resp proto.Message) {
	out, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(resp)
	if err != nil {
		writeError(w, h.logger, principal, method, status.Error(codes.Internal, "marshal response"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

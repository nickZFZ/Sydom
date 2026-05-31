MIGRATIONS := db/migrations
# 用法：make migrate-up DSN='postgres://sydom:sydom@localhost:5432/sydom?sslmode=disable'
migrate-up:
	go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate \
		-path $(MIGRATIONS) -database '$(DSN)' up

migrate-down:
	go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate \
		-path $(MIGRATIONS) -database '$(DSN)' down

test:
	go test ./... -v

GOBIN := $(shell go env GOPATH)/bin
BUF_VERSION := v1.34.0

# 安装 proto 工具链（buf 自带 protocompile，无需系统 protoc）
proto-tools:
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.33.0
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0

proto-lint:
	PATH="$(GOBIN):$$PATH" buf lint

# 先过 lint 再生成，避免产出 lint 不通过的代码
proto-gen: proto-lint
	PATH="$(GOBIN):$$PATH" buf generate

# CI 漂移检测：生成代码必须与 .proto 同步且已入库
proto-check: proto-gen
	git diff --exit-code gen/

.PHONY: migrate-up migrate-down test proto-tools proto-lint proto-gen proto-check

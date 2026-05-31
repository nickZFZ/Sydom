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

.PHONY: migrate-up migrate-down test

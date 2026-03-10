.DEFAULT_GOAL := help
SHELL := /bin/bash
.PHONY: help proto dev start install-tools prod-build prod-up prod-down lint lint-fix

help:
	@echo "Available commands:"
	@echo "  make proto          - Generate protobuf code"
	@echo "  make dev            - Run in development mode (local storage)"
	@echo "  make start          - Start service with interactive backend selection"
	@echo "  make install-tools  - Install protoc plugins"
	@echo "  make prod-build     - Build Docker image"
	@echo "  make prod-up        - Start services with docker-compose"
	@echo "  make prod-down      - Stop services with docker-compose"
	@echo "  make lint           - Run golangci-lint"
	@echo "  make lint-fix       - Run golangci-lint with auto-fix"

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/imagestore/v1/imagestore.proto

install-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@command -v golangci-lint > /dev/null || (echo "Installing golangci-lint..." && \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin)

dev:
	go run ./cmd/server

start:
	@echo ""
	@echo "Select storage backend:"
	@echo "  1) Local filesystem (no AWS)"
	@echo "  2) S3 via LocalStack (dev)"
	@echo ""
	@read -p "Enter choice [1/2]: " choice; \
	case $$choice in \
		1) \
			echo ""; \
			echo "Starting image storage service with local filesystem backend..."; \
			echo ""; \
			STORAGE_BACKEND=local go run ./cmd/server \
			;; \
		2) \
			echo ""; \
			echo "Starting LocalStack..."; \
			docker-compose up -d localstack; \
			echo "Waiting for LocalStack to be healthy..."; \
			until curl -sf http://localhost:4566/_localstack/health > /dev/null 2>&1; do \
				echo "  Waiting for LocalStack..."; \
				sleep 2; \
			done; \
			echo "LocalStack is ready."; \
			echo ""; \
			echo "Starting image storage service with S3 (LocalStack) backend..."; \
			echo ""; \
			STORAGE_BACKEND=s3 \
			S3_BUCKET=imagestore-bucket \
			S3_REGION=us-east-1 \
			S3_ENDPOINT=http://localhost:4566 \
			AWS_ACCESS_KEY_ID=test \
			AWS_SECRET_ACCESS_KEY=test \
			go run ./cmd/server \
			;; \
		*) \
			echo "Invalid choice. Exiting."; \
			exit 1 \
			;; \
	esac

prod-build:
	docker-compose build

prod-up:
	docker-compose up -d

prod-down:
	docker-compose down

lint:
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

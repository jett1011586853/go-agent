@echo off
if "%1"=="test" (
    go test -v ./...
) else if "%1"=="deps" (
    go mod download && go mod tidy
) else if "%1"=="run" (
    go run ./cmd/server
) else if "%1"=="build" (
    go build -o bin/trade-lab.exe ./cmd/server
) else (
    echo Usage: make [test^|deps^|run^|build]
)

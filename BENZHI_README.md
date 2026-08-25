# BENZHI_README

这是一个基于 Go 实现的后端服务，用于承载 cutVideo 的业务处理、数据管理与稳定运行。

## 项目说明

- 项目：vance1852/cutVideo
- 项目用途：cutVideo is a backend service for cloud based video post production. It carries a cut from ingested camera footage to a distributed master: footage is registered and checksum verified, editors assemble ordered timeline versions, a sealed version is queued onto a shared render farm, background workers hold exclusive encoder seats while they render, and finished masters are pushed to downstream destinations with per destination bookkeeping.
- Go 工具链：`golang:1.22`
- 前端工具链：无

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-135-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-135-arm64 linux/arm64
docker run -it benzhi-task-135-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-135-arm64:latest
```

## 题目验证命令

1. 预期退出码 0：`go test ./internal/app -run '^TestClaimStaysInsideItsRenderPool$' -count=1`
2. 预期退出码 0：`go test ./...`
3. 预期退出码 0：`GOTOOLCHAIN=local go build -buildvcs=false ./... && GOTOOLCHAIN=local go vet ./...`

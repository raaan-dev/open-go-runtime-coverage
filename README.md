# Open Go Runtime Coverage

一个可独立运行的 Go 需求级运行时覆盖率示例项目。

项目包含三个可复用部分：

- `coverage-hook`：嵌入业务服务，在测试环境提供运行时覆盖率快照接口。
- `coverage-report`：独立命令行工具，计算起始/结束快照的增量覆盖率并生成 HTML 报告。
- `service`：用于验证完整链路的 Go demo。

## 目录结构

```text
service/          Go 业务服务 demo
coverage-hook/    服务侧覆盖率采集组件
coverage-report/  CI/CD 阶段执行的报告工具
docs/             接入、运行和 CI/CD 说明
```

## 快速开始

```bash
cd service
go mod tidy
go build -cover -covermode=atomic -tags coverage_snapshot \
  -ldflags "-X main.coverageBuildSHA=local" -o /tmp/open-coverage-demo .
```

启动测试实例：

```bash
export COVERAGE_SNAPSHOT_ENABLED=true
export COVERAGE_SNAPSHOT_ADDR=127.0.0.1:18086
export COVERAGE_SNAPSHOT_DIR=/tmp/open-coverage-snapshots
export COVERAGE_SNAPSHOT_TOKEN=01234567890123456789012345678901
export COVERAGE_DEPLOYMENT_ID=local-demo-1
export GOCOVERDIR=/tmp/open-coverage-runtime
rm -rf "$COVERAGE_SNAPSHOT_DIR" "$GOCOVERDIR"
mkdir -p "$COVERAGE_SNAPSHOT_DIR" "$GOCOVERDIR"
PORT=18085 /tmp/open-coverage-demo
```

另开终端执行：

```bash
curl -X POST http://127.0.0.1:18086/internal/coverage/snapshot \
  -H 'Authorization: Bearer 01234567890123456789012345678901' \
  -H 'X-Snapshot-ID: start-001'
curl http://127.0.0.1:18085/health
curl -X POST http://127.0.0.1:18086/internal/coverage/snapshot \
  -H 'Authorization: Bearer 01234567890123456789012345678901' \
  -H 'X-Snapshot-ID: finish-001'
```

生成报告：

```bash
cd coverage-report
go run ./cmd/coverage_requirement_report \
  -repo ../service \
  -snapshots /tmp/open-coverage-snapshots \
  -start-id start-001 \
  -finish-id finish-001 \
  -out /tmp/open-coverage-report
open /tmp/open-coverage-report/index.html
```

## 设计边界

```text
业务服务 → coverage-hook → 运行时快照
CI/CD    → coverage-report → 增量覆盖率 HTML 报告
```

覆盖率端口只监听回环地址，Token 只通过环境变量注入；普通构建不启用快照功能。

## 详细文档

- [服务接入指南](docs/integration.md)
- [本地使用指南](docs/usage.md)
- [CI/CD 接入示例](docs/ci-cd.md)

# CI/CD 接入示例

流水线建议拆成：拉取源码 → 使用 `-cover -covermode=atomic` 构建 → 启动隔离测试实例 → 起始快照 → 执行测试 → 结束快照 → 调用 `coverage_requirement_report` → 发布 `index.html` artifact → 清理实例。

Jenkins shell 示例：

```bash
set -eux
BUILD_SHA=$(git rev-parse HEAD)
go build -cover -covermode=atomic -tags coverage_snapshot \
  -ldflags "-X main.coverageBuildSHA=$BUILD_SHA" \
  -o "$WORKSPACE/service-bin" ./service

go run ./coverage-report/cmd/coverage_requirement_report \
  -repo "$WORKSPACE/service" \
  -snapshots "$WORKSPACE/snapshots" \
  -start-id "$START_SNAPSHOT_ID" \
  -finish-id "$FINISH_SNAPSHOT_ID" \
  -out "$WORKSPACE/report"
```

不要把覆盖率控制端口暴露到公网；Kubernetes 中应通过 Pod 内 localhost、sidecar 或受控代理调用。

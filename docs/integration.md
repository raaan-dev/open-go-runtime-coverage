# 服务接入指南

## 引入组件

业务服务只需在启动入口调用一次 `coveragehook.Start`：

```go
stop, err := coveragehook.Start(coveragehook.Config{
    Enabled: os.Getenv("COVERAGE_SNAPSHOT_ENABLED") == "true",
    ListenAddress: os.Getenv("COVERAGE_SNAPSHOT_ADDR"),
    SnapshotDir: os.Getenv("COVERAGE_SNAPSHOT_DIR"),
    Token: os.Getenv("COVERAGE_SNAPSHOT_TOKEN"),
    BuildSHA: coverageBuildSHA,
    DeploymentID: os.Getenv("COVERAGE_DEPLOYMENT_ID"),
})
if err != nil { log.Fatal(err) }
defer stop(context.Background())
```

普通构建将 `COVERAGE_SNAPSHOT_ENABLED` 设为 `false` 或不设置即可关闭监听。

## 构建要求

必须使用原子计数器模式，否则快照接口无法写出 counters：

```bash
go build -cover -covermode=atomic -tags coverage_snapshot \
  -ldflags "-X main.coverageBuildSHA=$BUILD_SHA" -o service .
```

## 网络与安全

- 业务端口和快照端口分离；
- 快照端口只监听 `127.0.0.1`；
- 快照请求必须携带 Bearer Token；
- 快照目录使用服务账号私有、可写的绝对路径；
- 不要把 Token、Jenkins 凭据或生产地址写入代码。

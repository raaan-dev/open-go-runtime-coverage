# Go 服务运行覆盖率快照组件

这是一个供 Go 服务接入的轻量组件：在**测试环境**中按需保存当前进程的运行覆盖率快照，并记录快照对应的代码版本和部署信息，供 Jenkins 报告工具后续汇总分析。

本组件只负责服务内的快照接口和数据清单，不包含 Jenkins 报告生成器、报告页面或业务代码。

## 环境隔离

- 普通构建不启用快照功能。
- 覆盖率构建需要 `coverage_snapshot` 构建标签。
- 即使构建带有该标签，仍需显式设置 `COVERAGE_SNAPSHOT_ENABLED=true` 才会启动快照监听。
- 该开关只应在测试环境开启；预发和生产环境保持关闭。
- 覆盖率构建需使用 Go 1.20 或更高版本；本组件已使用 Go 1.20.1 验证。

## 服务接入

服务启动时调用 `coveragehook.Start`，配置从部署环境读取，不要把 Token 写入代码或仓库。控制接口只监听本机回环地址，快照目录应为服务账号私有且可写的绝对路径。

```go
stopCoverage, err := coveragehook.Start(coveragehook.Config{
	Enabled:       os.Getenv("COVERAGE_SNAPSHOT_ENABLED") == "true",
	ListenAddress: os.Getenv("COVERAGE_SNAPSHOT_ADDR"),
	SnapshotDir:   os.Getenv("COVERAGE_SNAPSHOT_DIR"),
	Token:         os.Getenv("COVERAGE_SNAPSHOT_TOKEN"),
	BuildSHA:      coverageBuildSHA,
	DeploymentID:  os.Getenv("COVERAGE_DEPLOYMENT_ID"),
})
if err != nil {
	log.Fatal(err)
}
defer func() { _ = stopCoverage(context.Background()) }()
```

覆盖率构建需设置 `GOCOVERDIR`，并在构建时把准确的 Git SHA 注入 `coverageBuildSHA`。快照请求使用 `POST /internal/coverage/snapshot`，携带 Bearer Token 和唯一的 `X-Snapshot-ID`。组件为每个快照单独建目录，只有覆盖率数据写入完成后才生成 `complete.json`。

## 本地验证

```sh
go test ./...
go test -tags coverage_snapshot ./...
```

第一条验证普通构建下功能关闭；第二条验证覆盖率快照处理逻辑。

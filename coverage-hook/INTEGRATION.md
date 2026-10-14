# Go 服务运行覆盖率接入说明

## 目标

业务服务只需引入本项目的 `coverage-hook` 模块，并在测试环境的覆盖率构建中开启快照开关，即可被 Jenkins 采集需求起止快照。普通构建、预发和生产环境默认不启动快照监听。

## 服务代码

在服务启动入口调用一次 `coveragehook.Start`，把配置全部交给环境变量；不要把 Token、快照目录或 Jenkins 地址写入业务代码。

```go
stop, err := coveragehook.Start(coveragehook.Config{
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
defer func() { _ = stop(context.Background()) }()
```

普通构建不需要 `coverage_snapshot` 标签；覆盖率构建使用：

```sh
go build -cover -tags coverage_snapshot \
  -ldflags "-X main.coverageBuildSHA=$(git rev-parse HEAD)" \
  -o service-coverage ./app
```

## 测试环境配置

| 变量 | 说明 |
| --- | --- |
| `COVERAGE_SNAPSHOT_ENABLED` | 仅测试覆盖率实例设为 `true`，其他环境设为 `false` 或不设置 |
| `COVERAGE_SNAPSHOT_ADDR` | 仅监听 `127.0.0.1:<port>`，不能暴露到外网 |
| `COVERAGE_SNAPSHOT_DIR` | 服务账号可写的绝对路径，每次快照使用独立子目录 |
| `COVERAGE_SNAPSHOT_TOKEN` | 由部署系统注入，不进入代码仓库或日志 |
| `COVERAGE_DEPLOYMENT_ID` | 当前部署实例的唯一标识 |
| `GOCOVERDIR` | 覆盖率构建的运行数据目录 |

业务端口和快照端口必须分开。快照接口只供 Jenkins 在服务所在机器上通过回环地址调用。

## Jenkins 流程

1. 构建覆盖率二进制并注入 Git SHA。
2. 将实例部署到测试环境的独立端口。
3. Skill 调用快照任务记录需求起点。
4. 执行需求测试流量。
5. Skill 调用快照任务记录需求终点。
6. 报告任务按基线 SHA 和部署分段生成需求增量覆盖率报告。

原有业务 Jenkins 任务不需要改造；建议为覆盖率实例、快照和报告分别创建隔离任务。

## 安全与故障边界

- 关闭快照开关时，组件不启动监听器，不影响普通服务。
- 快照失败不能导致业务请求停机；报告失败时保留起止快照，允许重试报告。
- 不停止正在运行的业务服务，不重置覆盖率计数器。
- 不同需求使用不同需求 ID，可并行采集。

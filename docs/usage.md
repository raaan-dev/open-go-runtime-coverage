# 本地使用指南

一次需求覆盖率窗口只有一组起止快照：

```text
起始快照 → 执行业务测试 → 结束快照 → 报告工具
```

起始和结束快照应来自同一个进程。若服务发生重新部署，必须记录 `deploy_*_start` 和 `deploy_*_prestop` 边界快照，报告工具才会按部署分段处理。

成功的快照目录会生成 `covmeta.*`、`covcounters.*` 和 `complete.json`。

报告目录包含 `index.html`、`report.json`、源码覆盖明细、函数统计和增量 profile。

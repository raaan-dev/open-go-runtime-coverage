# go-coverage-demo

用于验证 Jenkins 构建流程的最小 Go HTTP 服务示例。

当前阶段只包含普通业务服务代码，不接入覆盖率组件、不配置 Jenkins、不推送远端。

## 启动

```bash
go run .
```

默认监听 `8080`，也可以通过 `PORT` 修改：

```bash
PORT=18082 go run .
```

## 接口

```text
GET /health
GET /hello?name=Li
GET /orders/1001
GET /orders/paid-1001
GET /orders/not-found
GET /feature?mode=fast
GET /feature?mode=safe
```

示例：

```bash
curl http://127.0.0.1:8080/health
curl 'http://127.0.0.1:8080/hello?name=Li'
curl http://127.0.0.1:8080/orders/paid-1001
```

## 分支覆盖演示

`/orders/{id}` 包含正常、已支付、参数错误和订单不存在分支；`/feature` 包含 fast、safe 和不支持模式分支，后续可用于验证不同测试流量产生的覆盖率差异。

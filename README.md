# 古生物修复制备台

古生物修复制备台是一个使用 Go 编写的后端基线，用于记录化石标本、审批修复方案、回填制备进度，并在全部工序完成后办理研究借阅放行。

该服务只使用进程内仓储，便于在本机重复启动和检查。它不依赖数据库、远端接口或其他在线资源。

## 目录

- `cmd/paleo-prep-station`：HTTP 服务入口和监听参数。
- `cmd/workflowcheck`：通过真实 HTTP 处理器执行四条业务链路的本地验证命令。
- `internal/httpapi`：路由、请求解码、状态码映射和 JSON 输出。
- `internal/station`：跨领域对象的编排、事务边界和业务流程。
- `internal/domain`：标本、方案、进度和放行记录的状态与不变量。
- `internal/repository`：互斥保护的内存仓储和原子提交。
- `internal/contracts`：稳定错误模型和公共请求结构。

## 构建与运行

```bash
go build ./...
go run ./cmd/paleo-prep-station --addr 127.0.0.1:18133
```

服务启动后，`GET /healthz` 返回就绪状态。

## 主要接口

- `GET /specimens` 与 `POST /specimens`
- `POST /specimens/batch`：按清册批次登记标本
- `GET /specimens/{id}`
- `POST /specimens/{id}/plans`
- `GET /plans/{id}`
- `POST /plans/{id}/approval`
- `POST /plans/{id}/progress`
- `POST /specimens/{id}/release`

### 批次登记

`POST /specimens/batch` 接收一份清册：

```json
{
  "manifest_id": "MF-2026-018",
  "items": [
    {"catalog_number": "pps-2026-020", "field_label": "北坡-20", "matrix_kind": "粉砂岩"},
    {"catalog_number": "PPS-2026-021", "field_label": "北坡-21", "matrix_kind": "泥岩"}
  ]
}
```

- 目录号与清册编号在登记前统一去除首尾空白并转为大写后再做身份判断。
- 整份清册在同一个仓储事务内提交：任一条目非法、批内目录号重复、与库内已有目录号冲突，或清册编号已被不同内容占用时，整体失败且不会留下任何标本或半成品标识。
- 首次成功返回 `201`，响应包含 `manifest_id`、`replayed=false` 与全部标本；批次内每条标本与单条登记具有相同的 `registered` 初始状态、版本 1 和同一登记时刻。
- 使用相同清册编号和相同内容重复提交时幂等返回 `200`（`replayed=true`，返回首次提交的标本，不新建数据）；相同编号、不同内容返回 `409`。
- 失败响应在既有 `code`/`message` 之外附带 `details`，逐条给出 1-based 的条目序号、字段和原因码。

## 本地验证

每条业务链路都可以独立执行：

```bash
go run ./cmd/workflowcheck --workflow register-specimen
go run ./cmd/workflowcheck --workflow register-manifest
go run ./cmd/workflowcheck --workflow approve-prep-plan
go run ./cmd/workflowcheck --workflow log-preparation
go run ./cmd/workflowcheck --workflow release-for-study
```

这些检查会启动临时 HTTP 处理器并调用真实公共路由。

## 环境变量

项目不需要环境变量，也没有外部服务依赖。

## 测试边界

本基线按要求暂不生成单元测试、测试夹具或端到端测试文件。后续任务阶段会补充红绿验证测试，届时再替换或扩展当前的生产级链路检查。

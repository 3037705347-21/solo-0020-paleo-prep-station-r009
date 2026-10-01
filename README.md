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
- `POST /specimens/batch`：按清册整批登记标本
- `GET /specimens/{id}`
- `POST /specimens/{id}/plans`
- `GET /plans/{id}`
- `POST /plans/{id}/approval`
- `POST /plans/{id}/progress`
- `POST /specimens/{id}/release`

### 清册整批登记

`POST /specimens/batch` 接收客户端清册号 `manifest_id` 和 `items`（结构与单条登记一致）。
目录号与清册号都会先去除空白并转为大写，随后在同一事务内整体提交：

- 成功返回 `201`，`status` 为 `registered`，`items` 按提交顺序给出每条标本，
  初始状态、版本（`version=1`）和时间与单条登记完全相同。
- 同一清册号以完全相同内容重复提交时返回 `200`，`status` 为
  `already_registered`，并返回首次登记的同一批标本，不会新建任何记录。
- 同一清册号但内容不同返回 `409`（`manifest_content_conflict`）。
- 批内目录号重复返回 `409`（`duplicate_catalog_number_in_batch`），
  与库内已有目录号冲突返回 `409`（`duplicate_catalog_number`），
  单条字段非法返回 `400`（`invalid_batch_items`），错误体 `details`
  逐条给出条目下标、目录号、错误码与说明。
- 任何失败都整批回滚：不留下部分标本或标本标识；失败不占用清册号，
  可修正后以同一清册号重新提交。

## 本地验证

每条业务链路都可以独立执行：

```bash
go run ./cmd/workflowcheck --workflow register-specimen
go run ./cmd/workflowcheck --workflow register-specimen-batch
go run ./cmd/workflowcheck --workflow register-specimen-batch-atomic
go run ./cmd/workflowcheck --workflow approve-prep-plan
go run ./cmd/workflowcheck --workflow log-preparation
go run ./cmd/workflowcheck --workflow release-for-study
```

这些检查会启动临时 HTTP 处理器并调用真实公共路由。

## 环境变量

项目不需要环境变量，也没有外部服务依赖。

## 测试边界

本基线按要求暂不生成单元测试、测试夹具或端到端测试文件。后续任务阶段会补充红绿验证测试，届时再替换或扩展当前的生产级链路检查。

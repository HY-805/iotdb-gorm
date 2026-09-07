# iotdb-gorm 双模型适配开发方案

> 目标仓库：`github.com/HY-805/iotdb-gorm`
> 编写日期：2026-09-07
> 当前阶段：核心实现已完成，正在进行兼容性收尾；IoTDB 1.3.1 真机测试被服务端 802 登录错误阻断，IoTDB 2.0.10 环境待接入

## 1. 目标与固定约束

- Go 固定为 `1.23.2`，不因本组件升级平台 Go 版本。
- GORM 基线固定为 `gorm.io/gorm v1.23.4`，不得隐式升级平台 GORM。
- 服务端兼容矩阵：IoTDB `1.3.1` TreeModel、IoTDB `2.0.10` TableModel。
- `ModelMode` 必须支持显式配置，零值和默认值均为 `TreeModel`。
- 官方客户端优先使用 `github.com/apache/iotdb-client-go/v2 v2.0.8`：该版本要求 Go 1.13，并同时提供 Tree SessionPool、TableSessionPool 和 Tablet API。
- 官方客户端 `v2.0.10` 要求 Go 1.25，本项目不采用。
- 先用单一官方客户端真实验证两种服务端；若 `v2.0.8` 无法兼容 Server 1.3.1，再拆分为 v1 Tree 客户端和 v2 Table 客户端。

## 2. 项目定位

`iotdb-gorm` 是 Apache 官方 `iotdb-client-go` 之上的 GORM 适配层，不重新实现 IoTDB RPC 协议、连接池、Tablet 编码或故障重连。

应用侧尽量使用：

```text
gorm.Open -> Table/Where/Select/Create/Find/CreateInBatches
```

组件内部负责把 GORM 语义转换为对应模型操作：

```text
GORM
  -> 公共 Dialector / Callback / Scanner
      -> TreeModel Backend  -> 官方 SessionPool
      -> TableModel Backend -> 官方 TableSessionPool
```

查询最终仍需调用官方客户端的查询 SQL 接口，因此目标是减少业务代码中的原生 SQL，而不是承诺组件内部完全没有 SQL。数据库专用能力必须显式隔离，不能伪装成通用 GORM 行为。

## 3. 上游源码导入

1. 保留当前个人仓库的 `main` 历史。
2. 已将 `wkk778/gorm-iotdb` 的固定提交 `757387622937a20c1c74e36ef0b0ab51ae2807c9` 作为一次上游源码导入。
3. 已增加只读 `upstream` 远端，后续仅手工评估同步，不自动合并。
4. 已保留 Apache License 2.0，并增加 `UPSTREAM.md` 记录来源提交和差异。
5. 模块路径已调整为 `github.com/HY-805/iotdb-gorm`。

原仓库中可复用 Dialector、GORM schema 解析、结果扫描和测试框架；原有 Migrator、嵌套 SessionPool、事务伪实现、参数拼接和批量 Create 路径需要重构。

## 4. 公共配置与 API

核心配置包含：

- `ModelMode`：`TreeModel` 或 `TableModel`，默认 `TreeModel`；
- `NodeURLs`、用户名、密码、Database；
- `Aligned`：TreeModel 默认 `true`；
- 连接池大小、连接超时、获取 Session 超时、查询超时；
- 单批最大行数、单批最大字节数、跨设备最大并发数；
- `AllowSQLFallback`：默认 `false`，禁止批量写入静默退化为逐行 SQL。

保留上游的 `Open(dsn)` 和 `New(Config)` 入口。DSN 未指定模型时进入 TreeModel；推荐平台使用结构化 `Config`，避免密码和复杂参数拼接。

模式校验：

- TreeModel 的 Database 必须是受控的 `root.xxx` 路径；
- TableModel 的 Database 按表模型数据库标识符校验；
- 路径和标识符只允许内部规则生成或通过严格白名单校验。

## 5. TreeModel 映射

固定映射规则：

```text
Config.Database = root.datacenter_compatible
db.Table("device001")
  -> root.datacenter_compatible.device001

Time 字段       -> IoTDB Time
其他数据字段    -> Measurement
完整 Device Path -> 仅允许位于配置的 Database 根路径下
```

TDengine TAG 不自动转换为 IoTDB TAG。标签、属性、模板等能力通过 TreeModel 扩展接口提供，避免产生错误的一一映射。

## 6. 双模型 Migrator

### 6.1 TreeMigrator

- 重写 `AutoMigrate`，不生成 `CREATE TABLE`。
- 优先调用官方 `CreateTimeseries`、`CreateAlignedTimeseries`、`CreateMultiTimeseries` 等 API。
- 根据 GORM struct 解析 Device、Time、Measurement、数据类型、编码和压缩配置。
- `Aligned=true` 时按设备创建对齐测点；已存在设备的对齐属性不一致时直接报错。
- 迁移只允许幂等创建和增加缺失测点；类型冲突直接报错。
- 首期禁止自动 Drop、Rename、类型修改和路径迁移。

### 6.2 TableMigrator

- 使用 TableSessionPool，并生成 IoTDB 2.0.10 表模型 DDL。
- 支持数据库、普通列、TAG、ATTRIBUTE、FIELD 和时间列的模式映射。
- 只执行幂等创建和兼容性加列；不执行破坏性迁移。
- 所有 DDL 在真实 2.0.10 环境中校验后才进入支持矩阵。

## 7. 批量写入优化

重写 GORM Create Callback，批量路径不再委托默认逐行 INSERT：

### TreeModel

1. 按 Device Path、对齐属性和测点 Schema 分组。
2. 在副本中按时间排序，校验重复时间戳、类型和 NULL 位图。
3. 构造官方 `Tablet`，按最大行数和最大字节数分批。
4. 单设备使用 `InsertAlignedTablet`/`InsertTablet`。
5. 多设备优先使用 `InsertAlignedTablets`/`InsertTablets`，减少 RPC 次数。

### TableModel

1. 按 Database、Table 和 Schema 分组。
2. 使用官方 `NewRelationalTablet` 构造关系 Tablet。
3. 从 TableSessionPool 获取 Session 后执行 `Insert`。
4. 多表写入使用有上限的并发，不按每行启动协程。

公共要求：

- `Create` 单条和切片均可使用；`CreateInBatches` 明确控制批大小。
- 不创建第二层自定义连接池；每个 Dialector 只持有一个官方池。
- 解析完整错误，附带模式、设备/表、批次行数和可安全输出的定位信息。
- 写入重试必须说明幂等边界；服务端已成功但客户端未收到响应时可能发生重复提交。

## 8. 首期 GORM 支持边界

首期支持并进行真实验证：

- `Table`、`Select`、`Where`、`Order`、`Limit`；
- `Find`、`Scan`、`Raw`、`Exec`、`WithContext`；
- `Create`、`CreateInBatches`；
- 双模型 `AutoMigrate` 的非破坏性子集。

首期明确不支持或直接返回错误：

- 关联、Preload、Join、外键、唯一索引；
- 跨设备/跨表事务、SavePoint、RollbackTo；
- 把 GORM `Updates`/`Save` 直接解释成关系库更新；
- 未登记的 TDengine `STABLE`、子表、TAG、`last_row` 专用 SQL。

GORM 初始化默认要求 `SkipDefaultTransaction: true`。驱动不能再用空 `Commit` 或关闭连接的 `Rollback` 伪装事务成功。

## 9. 查询、参数和结果扫描

- GORM 生成查询条件，适配层按模型生成 SQL，再交给官方查询接口。
- 值参数使用可测试的字面量编码器；动态路径和列名不允许作为普通值参数拼接。
- TreeModel 和 TableModel 分别实现引用规则，不能共用普通关系库引号逻辑。
- 完整覆盖 BOOLEAN、INT32、INT64、FLOAT、DOUBLE、TEXT/STRING、TIMESTAMP、DATE、BLOB 和 NULL。
- SessionDataSet 必须及时关闭并归还 Session，读取错误不能静默转换为零值。
- 官方 `v2.0.8` 部分调用内部使用 `context.Background()`；`WithContext` 的取消能力需要在测试报告中说明真实边界，不能宣称可中断所有在途 RPC。

## 10. 测试与性能验证

### 10.1 本地测试

- 配置、模式默认值和 DSN 解析；
- GORM schema 到双模型 Schema 的转换；
- 路径、标识符、类型、NULL 和时间转换；
- Migrator 幂等与冲突行为；
- Tablet 分组、排序、分批和错误处理；
- GORM DryRun SQL；
- 事务和不支持能力必须显式失败。

执行：

```bash
go test ./...
go test -race ./...
go vet ./...
```

### 10.2 真实兼容矩阵

集成测试通过环境变量提供地址和密码，普通 `go test ./...` 不访问远端：

| 服务端 | 模型 | 必测内容 |
|---|---|---|
| IoTDB 1.3.1 | TreeModel | 连接、建模、Aligned/非 Aligned Tablet、批量写入、时间范围、最新值、类型与 NULL、删除测试数据、重连 |
| IoTDB 2.0.10 | TableModel | 连接、建库建表、RelationalTablet、批量写入、条件查询、类型与 NULL、删除测试数据、重连 |

测试数据使用唯一前缀，只清理本轮创建的路径或表。未提供环境时测试必须明确 Skip，不得伪装通过。

### 10.3 Benchmark

对 1、100、1000、5000 行分别比较：

- GORM 逐条 Create；
- GORM CreateInBatches -> Tablet；
- 官方客户端直接 Tablet 基线；
- 单设备与多设备；
- Aligned 与非 Aligned。

输出吞吐量、P50/P95、内存分配、RPC 次数和失败率。验收重点是批量链路无逐行 RPC，并量化 GORM 封装相对官方客户端的开销。

## 11. 开发阶段

1. **上游导入**：已完成固定提交导入、模块路径调整、许可证和来源说明保留。
2. **依赖收敛**：已固定 Go 1.23.2、GORM 1.23.4、官方客户端 v2.0.8，并加入固定依赖检查。
3. **连接层重构**：已统一配置；Tree/Table 分别使用官方 SessionPool/TableSessionPool，移除嵌套池和伪事务。
4. **TreeModel**：已完成路径映射、TreeMigrator、查询扫描和 Tablet 写入；真机验证被服务端 802 登录错误阻断。
5. **TableModel**：已完成 TableMigrator、关系 Tablet 和查询扫描；待 IoTDB 2.0.10 环境进行真机验证。
6. **兼容与性能**：本地单元测试、race、vet 和 Tablet 转换 benchmark 已通过；双版本真机报告尚未闭环。
7. **发布**：待 1.3.1 登录问题和 2.0.10 真机验证完成后，再发布稳定 tag；平台只引用明确 tag，不引用 `main`。
8. **平台试接入**：先接入独立 `iotdb_compatible` 链路，不立即替换 `global.GVA_DB_TD`。

## 12. 验收标准与回滚

- Go 1.23.2 下构建、单元测试、race 和 vet 通过。
- `go list -m` 确认 GORM 仍为 v1.23.4。
- TreeModel 默认值、完整路径限制和非破坏性迁移生效。
- 两个服务端的真实测试报告均通过后，才声明双版本兼容。
- 批量 GORM 写入实际进入官方 Tablet API，并提供与官方直接调用的基准对比。
- 不支持的 GORM 能力返回清晰错误，不静默成功或降级。
- 平台通过固定版本 tag 引入；回滚时只需退回旧 tag，现有 TDengine 链路不受影响。

## 13. 当前阻断与待补信息

- IoTDB 2.0.10 TableModel 测试环境的 NodeURL、Database 和账号；
- GitHub CLI 登录授权，首次推送前执行；
- IoTDB 1.3.1 端点当前返回 `error code: 802, Log in failed`；同源使用官方 v1.3.7 Tree 客户端也返回 802，需先核对账号、密码、服务端会话和 host 网络配置；
- 认证问题排除后，重新执行 TreeModel 真机测试，再决定是否继续统一使用官方客户端 v2.0.8，或启动双客户端兜底方案。

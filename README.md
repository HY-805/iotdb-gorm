# iotdb-gorm

[![CI](https://github.com/HY-805/iotdb-gorm/actions/workflows/ci.yml/badge.svg)](https://github.com/HY-805/iotdb-gorm/actions/workflows/ci.yml)

`iotdb-gorm` 是 Apache 官方 [`iotdb-client-go`](https://github.com/apache/iotdb-client-go) 之上的薄 GORM 适配层。它不复制 RPC 协议；GORM 的批量 `Create` 会直接转换成官方 Tablet API。

固定依赖与目标矩阵：

- Go：`1.23.2`
- GORM：`gorm.io/gorm v1.23.4`
- TreeModel 查询：`github.com/apache/iotdb-client-go v1.3.7`，适配 IoTDB `1.3.1`
- Tablet 写入与 TableModel：`github.com/apache/iotdb-client-go/v2 v2.0.8`，已验证 IoTDB `2.0.8` TableModel；IoTDB `2.0.10` 仍需独立真机确认

> 当前状态：IoTDB 1.3.1 TreeModel 和 IoTDB 2.0.8 TableModel 真机集成测试已通过。IoTDB 2.0.10 尚未真机验证，不能将该版本写为已验收。

## 安装

```bash
go get github.com/HY-805/iotdb-gorm@<固定版本标签>
```

平台项目应引用发布标签，不应长期引用 `main`。

完整的连接、建模、迁移、批量写入、查询和 Raw SQL 示例见 [使用指南](docs/README.md)。

## TreeModel 快速开始

```go
package main

import (
    "log"
    "time"

    gormiotdb "github.com/HY-805/iotdb-gorm"
    "gorm.io/gorm"
)

type Telemetry struct {
    Time        time.Time `gorm:"column:time" iotdb:"time"`
    Temperature float64   `gorm:"column:temperature"`
    Online      bool      `gorm:"column:online"`
}

func main() {
    db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
        ModelMode: gormiotdb.TreeModel,
        NodeURLs:  []string{"127.0.0.1:6667"},
        Username:  "root",
        Password:  "root",
        Database:  "root.datacenter_compatible",
        // Aligned 省略时默认为 true。
    }), &gorm.Config{
        SkipDefaultTransaction: true,
    })
    if err != nil {
        log.Fatal(err)
    }
    defer gormiotdb.Close(db)

    device := db.Table("device001")
    if err := device.AutoMigrate(&Telemetry{}); err != nil {
        log.Fatal(err)
    }

    rows := []Telemetry{
        {Time: time.Now(), Temperature: 20.5, Online: true},
        {Time: time.Now().Add(time.Millisecond), Temperature: 20.8, Online: true},
    }
    if err := device.CreateInBatches(&rows, 1000).Error; err != nil {
        log.Fatal(err)
    }
}
```

TreeModel 固定路径规则：

```text
Config.Database = root.datacenter_compatible
db.Table("device001")
  -> root.datacenter_compatible.device001
```

传入完整路径时，该路径必须位于配置根路径下。动态路径节点只接受安全标识符，不支持表别名或 SQL 表达式。

TreeModel只读查询支持完整节点通配符`*`和`**`。通配符匹配多个设备时，服务端返回的完整测点路径会保留为独立列；由于列名是动态的，应使用`map[string]any`接收：

```go
var rows []map[string]any
err := db.Table("root.datacenter_compatible.**.product_data").
    Select("pressure").
    Where("time >= ? AND time < ?", startMillis, endMillis).
    Order("time asc").
    Find(&rows).Error
```

结果中的测点列名类似`root.datacenter_compatible.device001.product_data.pressure`。通配符只允许作为完整路径节点，`device**`等写法会被拒绝；`Create`、`CreateInBatches`和`AutoMigrate`仍只接受具体设备路径。

### 多设备批量写入

模型可以使用一个显式设备字段。该字段只负责路由，不会写成 measurement：

```go
type Telemetry struct {
    Time       time.Time `gorm:"column:time" iotdb:"time"`
    DevicePath string    `gorm:"column:device_path" iotdb:"device"`
    Value      float64   `gorm:"column:value"`
}
```

同一次 `Create` 中的数据会按设备分组、按时间排序并按边界拆分：

- 单设备：`InsertAlignedTablet` 或 `InsertTablet`
- 多设备：`InsertAlignedTablets` 或 `InsertTablets`
- `Aligned` 默认 `true`；非对齐使用 `Aligned: gormiotdb.Bool(false)`
- 默认每个 Tablet 最多 1000 行、估算序列化数据最多 4 MiB
- 重复设备时间戳会直接报错，不静默覆盖

## TableModel 示例

```go
type TableTelemetry struct {
    Time        time.Time `gorm:"column:time" iotdb:"time"`
    Region      string    `gorm:"column:region" iotdb:"tag"`
    DeviceID    string    `gorm:"column:device_id" iotdb:"tag"`
    Description string    `gorm:"column:description" iotdb:"attribute"`
    Value       float64   `gorm:"column:value" iotdb:"field"`
}

db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
    ModelMode: gormiotdb.TableModel,
    NodeURLs:  []string{"127.0.0.1:6667"},
    Username:  "root",
    Password:  "root",
    Database:  "iotdb_test",
}), &gorm.Config{SkipDefaultTransaction: true})
```

TableModel 使用官方 `NewRelationalTablet` 和 `TableSessionPool.Insert`。TAG、ATTRIBUTE、FIELD 必须显式标记；这些角色不会在 TreeModel 中被偷偷转换为 measurement。

## 配置

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `ModelMode` | `TreeModel` | `TreeModel` 或 `TableModel` |
| `NodeURLs` | 无 | 官方客户端节点列表，至少一个 `host:port` |
| `Database` | 无 | TreeModel 根路径或 TableModel database |
| `Aligned` | `true` | TreeModel 对齐写入，使用 `*bool` 区分未配置和 false |
| `PoolSize` | `8` | 唯一官方 SessionPool 的最大 Session 数 |
| `ConnectTimeout` | `10s` | 建连超时 |
| `AcquireTimeout` | `10s` | 获取 Session 超时 |
| `QueryTimeout` | `30s` | 服务端查询超时 |
| `BatchSize` | `1000` | 单 Tablet 最大行数 |
| `MaxBatchBytes` | `4 MiB` | 单 Tablet 估算数据上限 |
| `TableInsertConcurrency` | `4` | TableModel 多 Tablet 最大并发数 |
| `TimePrecision` | `Milliseconds` | `time.Time` 转换精度 |
| `DevicePathField` | 空 | 不使用 tag 时可按字段名指定 TreeModel 设备路径 |
| `AllowSQLFallback` | `false` | 是否允许自定义 Conn 走 SQL INSERT；生产建议保持关闭 |

也可使用 DSN：

```text
iotdb://root:root@127.0.0.1:6667/root.datacenter_compatible?model=tree&aligned=true&batch_size=1000
```

生产配置建议使用结构化 `Config`，避免密码进入日志或进程参数。

## 首期 GORM 能力边界

已实现或纳入集成测试：

- `Table`、`Select`、`Where`、`Order`、`Limit`
- `Find`、`Scan`、`Raw`、`Exec`、`WithContext`
- `Create`、`CreateInBatches`
- TreeModel 与 TableModel 的非破坏性 `AutoMigrate`

显式不支持：

- 默认事务、手工事务、SavePoint、RollbackTo
- `Updates`、`Save` 的关系更新语义
- GORM `Delete`；删除数据需显式使用 IoTDB SQL/API
- Association、Preload、Join、外键、唯一索引
- 自动 Drop、Rename、类型修改
- 把 TDengine STABLE、子表、TAG、`last_row` 自动伪装成通用 GORM 语义

必须配置 `SkipDefaultTransaction: true`。组件不会用空 `Commit`/`Rollback` 冒充事务成功。

## 生命周期和 Context

TableModel 每个 Dialector 使用一个官方 `TableSessionPool`。为兼容 IoTDB 1.3.1，TreeModel 使用官方 v2 SessionPool 完成 Tablet 写入和 schema API，并使用官方 v1 SessionPool 读取结果；两者均由同一 Dialector 生命周期统一关闭。应用退出时必须调用：

```go
if err := gormiotdb.Close(db); err != nil {
    // 记录并处理关闭错误
}
```

官方 v1/v2 的部分 API 内部仍使用 `context.Background()`。`WithContext` 可以阻止尚未开始的操作并约束查询超时，但不能承诺中断所有已经发出的 RPC。

### IoTDB 1.3.1 TreeModel 查询边界

- 时间戳是服务端隐式返回列，`Select(...)` 中不要显式写 `time`；使用 `Where("time >= ?", ...)` 和 `Order("time asc")` 限定时间即可。
- `count(*)` 返回各 measurement 的聚合结果，不等价于关系表行数，不能直接用 GORM `Count` 表示设备记录数。
- 服务端会把全 NULL 的 TEXT 结果返回为空 Binary；适配层将零长度 Binary 读取为 `nil`，因此 TreeModel 下空字符串和 NULL 不可区分。

## 测试

普通测试不访问外部数据库：

```bash
go test ./...
go test -race ./...
go vet ./...
```

IoTDB 1.3.1 TreeModel 真机测试：

```bash
IOTDB_TREE_INTEGRATION=1 \
IOTDB_TREE_NODE_URL=127.0.0.1:6667 \
IOTDB_TREE_DATABASE=root.datacenter_compatible \
IOTDB_TREE_SECOND_DATABASE=root.systemcenter_compatible \
go test ./tests -run '^TestTreeIntegration' -count=1 -v
```

IoTDB 2.0.8 TableModel 真机测试：

```bash
IOTDB_TABLE_INTEGRATION=1 \
IOTDB_TABLE_NODE_URL=127.0.0.1:6667 \
IOTDB_TABLE_DATABASE=iotdb_test \
go test ./tests -run '^TestTableIntegration' -count=1 -v
```

测试只删除本轮生成的唯一设备路径或表。未配置开关时会明确 Skip，不会把未执行写成通过。

## 目录

- `dialector/`：配置、GORM callbacks、双模型 Migrator、Tablet 映射
- `driver/iotdbsql/`：共享官方池上的轻量 `database/sql` bridge
- `internal/backend/`：官方 Tree/Table SessionPool 生命周期
- `tests/`：显式开关控制的真实版本矩阵测试
- `docs/README.md`：完整使用指南和兼容边界

上游来源和本仓库差异见 [UPSTREAM.md](./UPSTREAM.md)。

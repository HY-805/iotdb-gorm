# IoTDB-GORM 能力边界说明文档

本文面向第一次接触 `github.com/HY-805/iotdb-gorm` 的 Go 开发者，用于回答三个问题：驱动能够完成哪些 IoTDB 操作、应该使用哪个 GORM API、哪些关系型数据库习惯不能直接套用。

文档以 `iotdb-gorm v0.1.2` 为基线，主要支持目标是 IoTDB 1.3.1 TreeModel。驱动包含 IoTDB 2.0.x TableModel 适配代码，但只在 IoTDB 2.0.8 上完成了基础链路验证，尚不足以声明全面支持 2.0.x TableModel。未明确列出的 IoTDB 或 GORM 能力不应视为已经支持。

版本定位如下：

- **主要支持：IoTDB 1.3.1 TreeModel。** 本文的路径、对齐 Device、measurement、通配符、聚合以及 TreeModel 元数据说明均以该模式为主。
- **有限验证：IoTDB 2.0.8 TableModel。** 已验证基础建表、写入和查询链路；接入 2.0.x 生产环境前必须补充目标版本和目标 API 的专项测试。

## 1. 阅读约定

### 1.1 能力状态

| 状态 | 含义 | 接入建议 |
|---|---|---|
| 支持并已验证 | 驱动已实现，并已在本章注明的真实 IoTDB 版本上验证结果 | 可按示例使用，升级驱动或服务端后仍需回归 |
| 支持 | 驱动有明确实现，但并非所有参数组合都经过真实服务端验证 | 使用前为目标组合补充集成测试 |
| 有限验证 | 只验证了指定版本的一条或少量基础调用链，不能代表该模式的完整能力 | 仅用于评估或补充测试，不直接作为生产兼容结论 |
| 需要原生 SQL | GORM 没有对应抽象，但驱动允许通过 `Exec` 或 `Raw` 发送 IoTDB SQL | 固定 SQL 结构，校验动态标识符，参数化业务值 |
| 不支持 | 驱动会返回错误，或 IoTDB 不具备可安全映射的语义 | 使用文档给出的替代方案 |
| 尚未验证 | 没有足够证据确认其生成 SQL、返回列或副作用 | 不要从相似 API 的结果推断可用性 |

“支持并已验证”只说明指定版本上的功能结果，不等同于多节点高可用、性能容量、长时间稳定运行或生产环境验收。

### 1.2 版本范围

| 组件 | 本文基线 | 说明 |
|---|---|---|
| Go | 1.23.2 | 仓库声明的 Go 版本 |
| GORM | 1.23.4 | 驱动适配的 GORM 版本 |
| iotdb-gorm | v0.1.2 | 本文描述的驱动版本 |
| TreeModel 查询客户端 | `iotdb-client-go v1.3.7` | 用于 IoTDB 1.3.1 TreeModel 查询 |
| TreeModel Schema 和写入客户端 | `iotdb-client-go/v2 v2.0.8` | 用于 Schema RPC 和 Tablet 写入 |
| TreeModel 服务端 | IoTDB 1.3.1 | 本文 TreeModel 验证边界 |
| TableModel 服务端 | IoTDB 2.0.8 | 只完成基础链路验证；不能外推到全部 2.0.x 版本或全部 TableModel API |

## 2. 先理解 IoTDB 数据模型

### 2.1 TreeModel 中的 Device 和 measurement

TreeModel 使用层级路径保存数据。完整测点路径由 Device 路径和 measurement 名组成：

```text
root.example.device_001.metrics_a.pressure
└──────────── Device ─────────────┘ └measurement┘
```

对 `iotdb-gorm` 而言，`Table()` 在 TreeModel 中表示 IoTDB Device，不是关系型数据库中的表。`pressure`、`temperature` 等结构体字段映射为该 Device 下的 measurement。

业务上的一台设备可以对应一个 IoTDB 子树，其中包含多个 IoTDB Device：

```text
业务设备根路径：root.example.device_001
├── device_profile       measurement，所属 IoTDB Device 为 root.example.device_001
├── metrics_a.pressure   measurement，所属 IoTDB Device 为 root.example.device_001.metrics_a
└── metrics_b.speed      measurement，所属 IoTDB Device 为 root.example.device_001.metrics_b
```

因此，`metrics_a` 和 `metrics_b` 在业务上可以属于同一台设备，但在 IoTDB 中是两个独立 Device。对齐、Tablet 写入和固定 Device 查询都以 IoTDB Device 为边界，不会自动跨指标分组生效。

### 2.2 对齐 Device 的实际含义

对齐表示同一个 IoTDB Device 下的多个 measurement 共享时间列和写入行。它不是业务层标签，也不会把同一子树下的多个 Device 合并为一个 Device。

- `root.example.device_001.metrics_a` 内的 `pressure` 和 `temperature` 可以对齐。
- `metrics_a.pressure` 与 `metrics_b.speed` 不属于同一个 IoTDB Device，不能依靠对齐关系一次读取或写入。
- 同一个 IoTDB Device 不能同时混建对齐与非对齐 measurement；`AutoMigrate` 检测到冲突时会返回错误。

### 2.3 TableModel 模式（有限验证）

TableModel 面向 IoTDB 2.x 的关系表模型。模型字段需要明确标记为 `time`、`tag`、`attribute` 或 `field`。TreeModel measurement 的 Tag 和 Attribute 是时序元数据，与 TableModel 的列角色不是同一概念，二者不能混用。

`iotdb-gorm v0.1.2` 只在 IoTDB 2.0.8 上验证了以下 TableModel 基础链路：

- 建立连接并执行 `gormiotdb.Ping`；
- 使用 `AutoMigrate` 创建包含 TAG、ATTRIBUTE 和 FIELD 的表；
- 使用 `CreateInBatches` 通过关系型 Tablet 写入多行数据；
- 写入和读取 NULL FIELD；
- 使用 `Where`、`Order` 和 `Find` 查询写入结果。

以下内容没有形成充分的 TableModel 兼容结论：其他 IoTDB 2.0.x 版本、完整 Migrator 生命周期、复杂聚合和查询表达式、元数据维护、并发容量、多节点故障切换、长时间稳定性及生产部署。因此，本文后续未明确标注 TableModel 的示例均按 IoTDB 1.3.1 TreeModel 理解。

## 3. 能力总览

除表中明确标注为 TableModel 的条目外，“支持并已验证”均指 IoTDB 1.3.1 TreeModel。

| 场景 | 状态 | 推荐入口 | 主要边界 |
|---|---|---|---|
| 建立连接、健康检查和关闭 | 支持并已验证 | `gorm.Open`、`gormiotdb.Ping`、`gormiotdb.Close` | 必须关闭驱动；不支持关系型事务 |
| TableModel 基础建表、写入和查询 | 有限验证 | `AutoMigrate`、`CreateInBatches`、`Where/Order/Find` | 只验证 IoTDB 2.0.8 基础链路，不代表完整支持 2.0.x |
| TreeModel 创建对齐或非对齐 measurement | 支持并已验证 | `Table(device).AutoMigrate(&Model{})` | 只新增缺失 measurement，不删除或改类型 |
| 单 Device 单条或批量写入 | 支持并已验证 | `Create`、`CreateInBatches` | Device 必须是准确路径 |
| 多 Device 批量写入 | 支持并已验证 | 带 `iotdb:"device"` 字段的 `CreateInBatches` | 批次内各行必须使用同一个结构体 Schema |
| 固定 Device 查询 | 支持并已验证 | `Table/Select/Where/Order/Limit/Find` | 不是完整的关系型查询能力 |
| TreeModel `*`、`**` 通配符查询 | 支持并已验证 | 只读 `Table()` 链式查询 | 动态结果使用 `[]map[string]any` |
| 聚合和固定时间窗口聚合 | 支持并已验证 | `Select` 或 `Raw` | 使用 IoTDB 函数语义，不使用 GORM 行数语义 |
| TreeModel alias、Tag、Attribute | 需要原生 SQL | `Exec("ALTER TIMESERIES ...")`、`Raw("SHOW TIMESERIES ...")` | `AutoMigrate` 不管理这些元数据 |
| 删除时间范围内的数据或删除 measurement | 需要原生 SQL | `Exec` | GORM `Delete()` 不支持；删除路径必须精确校验 |
| Join、关联、事务和关系型更新 | 不支持 | 分别查询后在应用层合并；新时间点使用 `Create` | 驱动主动拒绝不安全映射 |

## 4. 连接配置与生命周期

### 4.1 推荐连接方式

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
	ModelMode:       gormiotdb.TreeModel,
	NodeURLs:        []string{"10.0.0.11:6667", "10.0.0.12:6667"},
	Username:        "root",
	Password:        os.Getenv("IOTDB_PASSWORD"),
	Database:        "root.example",
	Aligned:         gormiotdb.Bool(true),
	PoolSize:        8,
	ConnectTimeout:  10 * time.Second,
	AcquireTimeout:  10 * time.Second,
	QueryTimeout:    30 * time.Second,
	ConnectRetryMax: 3,
	BatchSize:       1000,
	MaxBatchBytes:   4 << 20,
}), &gorm.Config{
	SkipDefaultTransaction: true,
})
if err != nil {
	return err
}
defer gormiotdb.Close(db)

if err := gormiotdb.Ping(ctx, db); err != nil {
	return fmt.Errorf("ping IoTDB: %w", err)
}
```

必须设置 `SkipDefaultTransaction: true`。IoTDB 没有与 GORM 默认关系型事务等价的提交和回滚语义；驱动会拒绝事务调用，不会用空操作伪装成功。

### 4.2 配置字段

| 配置 | 默认值 | 含义 | 使用约束 |
|---|---:|---|---|
| `DSN` | 空 | 使用 `iotdb://` URL 集中声明节点、认证、模型和连接参数 | 结构化配置中的非零字段会覆盖 DSN；生产环境避免把密码放入进程参数或日志 |
| `ModelMode` | `TreeModel` | 选择 TreeModel 或 TableModel | 一个 `*gorm.DB` 只对应一种模型 |
| `NodeURLs` | 无 | 官方客户端可以连接的节点地址列表，每项格式为 `host:port` | 未提供自定义 `Conn` 时至少配置一个地址；列表不代表业务请求一定会平均分配 |
| `Username` / `Password` | `root` / `root` | IoTDB 认证信息 | 生产环境从 Secret 或环境变量读取，不写入日志 |
| `Database` | 无 | TreeModel 的受控根路径，或 TableModel 的 database 名 | TreeModel 必须以 `root.` 开头；TableModel 必须是安全标识符 |
| `Aligned` | `true` | TreeModel Schema 和 Tablet 是否使用对齐接口 | 只有 `gormiotdb.Bool(false)` 能显式关闭；不能改变既有 Device 的对齐方式 |
| `PoolSize` | `8` | 单个官方 SessionPool 最多可借出的 Session 数 | TreeModel 内部有两个池，该上限分别作用于两个池，不是进程总连接数 |
| `ConnectTimeout` | `10s` | 官方 SessionPool 建立连接时使用的超时 | 不能替代业务请求 deadline |
| `AcquireTimeout` | `10s` | 池耗尽时等待可用 Session 的最长时间 | 未关闭的 `Rows` 会占用查询 Session，并可能导致后续请求超时 |
| `QueryTimeout` | `30s` | 发送给服务端的查询超时 | 与 Context deadline 同时存在时取更短值；不承诺中断已经发出的 Schema 或写入 RPC |
| `FetchSize` | `1024` | 查询结果每次从服务端获取的数据规模 | 需要根据结果列宽、网络和内存压测调整 |
| `TimeZone` | `Asia/Shanghai` | 创建官方客户端连接时使用的时区 | 驱动没有运行期 `SetTimeZone` / `GetTimeZone` API |
| `ConnectRetryMax` | `3` | 传给官方客户端的断线重连轮次 | 只影响连接恢复，不会自动重放失败查询或写入，也不保证写入恰好一次 |
| `EnableRPCCompression` | `false` | 是否启用官方客户端 RPC 压缩 | 应结合 CPU、网络和数据规模压测决定 |
| `BatchSize` | `1000` | 驱动构造的单个 Tablet 最大行数 | 按准确 Device 分组后生效，不等于 MQTT 消息条数 |
| `MaxBatchBytes` | `4 MiB` | 单个 Tablet 的估算数据大小上限 | 行数或字节数任一达到上限都会切分 Tablet |
| `TableInsertConcurrency` | `4` | TableModel 多 Tablet 写入的最大并发数 | 只作用于 TableModel；TreeModel 使用官方多 Tablet API |
| `TimePrecision` | `Milliseconds` | `time.Time` 转换为 IoTDB 时间戳时使用的精度 | 必须与服务端时间精度一致 |
| `DevicePathField` | 空 | 用字段名指定每行的 TreeModel Device 路径 | 与 `DevicePathFunc` 二选一；效果等同于该字段使用 `iotdb:"device"` |
| `DevicePathFunc` | 空 | 通过回调计算每行的 TreeModel Device 路径 | 返回值仍要通过驱动的路径校验；与 `DevicePathField` 二选一 |
| `AllowSQLFallback` | `false` | 自定义 `Conn` 且官方 Tablet 后端不可用时，允许 GORM 生成 SQL INSERT | 这是兼容逃生口，不是推荐生产写入路径；正常连接应保持关闭 |
| `Conn` | 空 | 注入由调用方管理的 `gorm.ConnPool` | 注入后驱动不创建官方客户端后端，因此 `Ping`、Tablet 写入和 TreeModel Schema RPC 不可用；只适合受控的兼容场景 |

也可以使用 DSN 创建驱动：

```go
db, err := gorm.Open(
	gormiotdb.Open(
		"iotdb://root:secret@10.0.0.11:6667,10.0.0.12:6667/root.example" +
			"?model=tree&aligned=true&pool_size=8&connect_retry_max=3",
	),
	&gorm.Config{SkipDefaultTransaction: true},
)
```

DSN 适合本地验证。生产环境推荐使用结构化 `Config` 并从 Secret 注入密码，避免认证信息出现在日志、命令历史或进程参数中。

### 4.3 `NodeURLs`、`PoolSize` 和 `ConnectRetryMax` 如何配合

`NodeURLs` 提供官方客户端创建或恢复 Session 时可尝试的节点地址。`ConnectRetryMax` 控制官方客户端的连接重试轮次。`iotdb-gorm` 不会在官方客户端之外再实现节点健康评分、熔断、请求级重试或失败批次重放。

TreeModel 为同时兼容 IoTDB 1.3.1 查询和 v2 Tablet API，会建立两个 SessionPool：

1. v2 SessionPool：用于 Schema 检查和 Tablet 写入；
2. v1 SessionPool：用于 TreeModel 查询和原生 SQL。

因此，`PoolSize: 8` 表示每个池各自最多借出 8 个 Session，不表示整个进程只会建立 8 条物理连接。TableModel 只使用一个 TableSessionPool。

配置多个 `NodeURLs` 只说明客户端拥有多个连接候选地址，不代表高可用已经成立。生产接入前还应验证首节点下线、已有连接中断、恢复时间、查询中断处理以及写入重试的幂等策略。

### 4.4 Context 与资源关闭

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

var rows []Telemetry
err := db.WithContext(ctx).
	Table("root.example.device_001.metrics_a").
	Where("time >= ? AND time < ?", begin, end).
	Find(&rows).Error
```

`WithContext` 可以阻止尚未开始的操作，并让查询采用 Context deadline 与 `QueryTimeout` 中更短的时限。官方客户端已经发出的部分 RPC 不保证能够立即取消，因此调用方仍需为超时后的重复写入制定幂等策略。

使用 `Rows()` 取得流式结果后必须关闭结果集：

```go
rows, err := db.Raw("SHOW TIMESERIES root.example.device_001.**").Rows()
if err != nil {
	return err
}
defer rows.Close()
```

未关闭的结果集会持续占用查询 Session，最终可能触发 `AcquireTimeout`。应用退出时还必须调用 `gormiotdb.Close(db)`，让驱动等待在途结果集归还并关闭其持有的官方连接池。

## 5. `Table()` 与 TreeModel 路径

### 5.1 运行时拼接准确 Device 路径

```go
databaseRoot := "root.example"
deviceID := "device_001"
group := "metrics_a"

// validatePathNode 是业务代码提供的白名单校验函数，不属于 iotdb-gorm。
if !validatePathNode(deviceID) || !validatePathNode(group) {
	return errors.New("invalid device path")
}
devicePath := databaseRoot + "." + deviceID + "." + group

var rows []Telemetry
err := db.Table(devicePath).
	Select("pressure", "temperature").
	Where("time >= ? AND time < ?", begin, end).
	Order("time ASC").
	Limit(1000).
	Find(&rows).Error
```

在 `Database: "root.example"` 下，`db.Table("device_001")` 会解析为 `root.example.device_001`。相对路径只允许一个节点；`device_001.metrics_a` 这种多层相对路径会被拒绝。多层 Device 应传入位于配置根路径内的完整路径，例如 `root.example.device_001.metrics_a`。

### 5.2 只读通配符路径

TreeModel 查询支持 `*` 和 `**` 作为完整路径节点：

```go
var rows []map[string]any
err := db.Table("root.example.**.metrics_a").
	Select("pressure").
	Where("time >= ? AND time < ?", begin, end).
	Order("time ASC").
	Limit(1000).
	Find(&rows).Error
```

当通配符匹配多个 Device 时，IoTDB 返回的每个完整测点路径会保留为独立列：

```text
time
root.example.device_001.metrics_a.pressure
root.example.device_002.metrics_a.pressure
```

列名由实际匹配结果决定，不能稳定映射到只有一个 `Pressure` 字段的结构体。因此应使用 `[]map[string]any` 接收，按完整路径读取测点值；结果中的 `time` 值类型为 `time.Time`。

通配符只允许独占一个路径节点。`device*`、`device**` 等组合写法会在发送 SQL 前被拒绝。通配符也只能用于查询，`Create`、`CreateInBatches` 和 `AutoMigrate` 始终要求准确 Device 路径。

### 5.3 表别名、子查询和任意表表达式

`Table()` 是受保护的 Device 路径入口，不是任意 SQL 的 `FROM` 表达式入口。以下写法会在 SQL 发送前返回错误：

```go
// 包含空格和别名，不是合法 Device 路径。
err := db.Table("root.example.device_001.metrics_a AS p").
	Find(&rows).Error

// 带变量的 TableExpr 属于子查询或动态表表达式。
subQuery := db.Table("root.example.device_001.metrics_a").Select("pressure")
err = db.Table("(?) AS p", subQuery).Find(&rows).Error
```

需要 IoTDB 专用查询结构时，可以改用 `Raw()`：

```go
var result []map[string]any
err := db.Raw(
	"SELECT last_value(pressure) FROM root.example.**.metrics_a "+
		"WHERE time >= ? AND time < ?",
	begin,
	end,
).Scan(&result).Error
```

`Raw()` 不执行 `Table()` 的根路径和标识符校验。SQL 关键字、路径和 measurement 名应由程序固定或从白名单构造，只有业务值使用参数占位符。

## 6. Schema、对齐测点与数据类型

### 6.1 使用 `AutoMigrate` 创建 measurement

```go
type ProductMetrics struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	Pressure    *float32  `gorm:"column:pressure" iotdb:"field;encoding=GORILLA;compression=SNAPPY"`
	Temperature *float64  `gorm:"column:temperature" iotdb:"field;encoding=GORILLA;compression=SNAPPY"`
	Online      *bool     `gorm:"column:online" iotdb:"field;encoding=RLE;compression=SNAPPY"`
}

device := "root.example.device_001.metrics_a"
if err := db.Table(device).AutoMigrate(&ProductMetrics{}); err != nil {
	return err
}
```

TreeModel 的 `AutoMigrate` 会读取现有 Device Schema，并执行以下规则：

- Device 不存在时，按配置的 `Aligned` 方式创建 measurements；
- Device 已存在时，只创建缺失 measurements；
- 已有 measurement 类型与模型不一致时返回冲突错误；
- 已有 Device 的对齐方式与配置不一致时返回冲突错误；
- 不删除、不重命名、不改变已有 measurement 类型。

这是一种非破坏性 Schema 同步，不是完整的数据库迁移系统。

### 6.2 Migrator API

| API | 状态 | 行为 | 限制或替代方案 |
|---|---|---|---|
| `AutoMigrate` | 支持并已验证 | 幂等创建缺失 Device/measurement，并检查类型与对齐方式 | 不执行删除、重命名和类型修改 |
| `CreateTable` | 支持 | TreeModel 创建 measurements；TableModel 创建表 | 目标名称必须通过相应模型的校验 |
| `HasTable`、`GetTables` | 支持 | 查询 IoTDB Device 或 TableModel 表 | 返回的是 IoTDB 对象，不是关系型表目录 |
| `AddColumn`、`HasColumn`、`ColumnTypes` | 支持 | 增加或检查 measurement/列，读取类型元数据 | 不能改变既有类型 |
| `DropTable`、`RenameTable` | 不支持 | 驱动返回 `gormiotdb.ErrUnsupportedOperation` | 使用独立、人工审核的 IoTDB 迁移方案 |
| `DropColumn`、`AlterColumn`、`RenameColumn` | 不支持 | 驱动返回 `gormiotdb.ErrUnsupportedOperation` | 先设计历史数据迁移和回滚，再执行明确的 IoTDB DDL |

### 6.3 TreeModel measurement 类型

IoTDB 1.3.1 TreeModel 已验证以下六种业务 measurement 类型：

| Go 字段类型 | 默认 IoTDB 类型 | 说明 |
|---|---|---|
| `bool` / `*bool` | `BOOLEAN` | 指针用于表达 NULL |
| `int32` / `*int32` | `INT32` | 其他整数可用 `iotdb:"type=INT32"` 明确声明并接受范围校验 |
| `int64` / `*int64` | `INT64` | 适合计数器等 64 位整数 |
| `float32` / `*float32` | `FLOAT` | 写入时检查溢出、NaN 和 Inf |
| `float64` / `*float64` | `DOUBLE` | 写入时拒绝 NaN 和 Inf |
| `string` / `*string` | `TEXT` | 也接受 `[]byte` 作为 TEXT 写入值 |

时间字段应使用 `time.Time` 并标记 `iotdb:"time"`。查询结果中的 `time` 也映射为 `time.Time`。`TimePrecision` 只控制写入和条件绑定时如何把 `time.Time` 转为整数时间戳。

IoTDB 1.3.1 没有由该驱动直接映射的数组 measurement 类型。功图等数组数据可以先序列化为 JSON，再写入 `TEXT`：

```go
type DiagramPoint struct {
	Time    time.Time `gorm:"column:time" iotdb:"time"`
	Diagram *string   `gorm:"column:diagram" iotdb:"field;type=TEXT"`
}
```

这种映射只保存完整 JSON 文本。数组元素筛选、数值聚合和局部更新需要在应用层完成。

### 6.4 TreeModel 元数据与 TableModel 列角色

TableModel 可以在结构体中声明 TAG、ATTRIBUTE 和 FIELD：

```go
type TableTelemetry struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	Region      string    `gorm:"column:region" iotdb:"tag"`
	DeviceID    string    `gorm:"column:device_id" iotdb:"tag"`
	Description string    `gorm:"column:description" iotdb:"attribute"`
	Temperature float64   `gorm:"column:temperature" iotdb:"field"`
}
```

TreeModel 的 Tag、Attribute 和 alias 属于 timeseries 元数据，不能通过这些结构体标签迁移。把 `iotdb:"tag"` 或 `iotdb:"attribute"` 用在 TreeModel 模型上会返回错误。TreeModel 元数据应按第 9 章使用明确的 IoTDB SQL 管理。

## 7. 数据写入

### 7.1 `Create` 和 `CreateInBatches`

| 调用方式 | 状态 | 驱动执行方式 | 约束 |
|---|---|---|---|
| `db.Table(device).Create(&row)` | 支持并已验证 | 构造一个 Tablet，调用 `InsertAlignedTablet` 或 `InsertTablet` | `device` 必须是准确路径 |
| `db.Table(device).Create(&rows)` | 支持并已验证 | 按 Device 和 Tablet 上限整理数据 | 结构体必须包含时间字段和至少一个 measurement |
| `CreateInBatches(&rows, batchSize)` | 支持并已验证 | GORM 先按参数切分模型切片，驱动再按 Device、行数和字节数构造 Tablet | 同一调用内，同一 Device 不能出现重复时间戳 |
| 带 `iotdb:"device"` 字段的批量写入 | 支持并已验证 | 按每行的 Device 路径分组，TreeModel 使用官方单/多 Tablet API | Device 字段只参与路由，不会写成 measurement |
| `result.RowsAffected` | 支持 | `Create` 成功时返回驱动处理的模型行数 | 不等于 measurement 点数，也不能代替写后查询 |

`CreateInBatches` 的 `batchSize` 参数控制 GORM 每次交给驱动的模型行数；`Config.BatchSize` 和 `MaxBatchBytes` 控制驱动构造的单个 Tablet 大小。两层限制都会生效。

### 7.2 多 Device 批量写入

```go
type RoutedMetrics struct {
	Time       time.Time `gorm:"column:time" iotdb:"time"`
	DevicePath string    `gorm:"column:device_path" iotdb:"device"`
	Pressure   *float32  `gorm:"column:pressure" iotdb:"field"`
}

rows := []RoutedMetrics{
	{Time: t0, DevicePath: "root.example.device_001.metrics_a", Pressure: ptr(float32(1.20))},
	{Time: t1, DevicePath: "root.example.device_002.metrics_a", Pressure: ptr(float32(1.30))},
}

if err := db.Table("logical_batch").CreateInBatches(&rows, 1000).Error; err != nil {
	return err
}

func ptr[T any](value T) *T { return &value }
```

`logical_batch` 只是所有行都缺少 Device 路径时的后备表名。示例中每行都提供了完整 `DevicePath`，驱动会分别路由到 `device_001.metrics_a` 和 `device_002.metrics_a`。多 Device 批次仍要求所有行使用同一个 Go 结构体 Schema；它不是任意 SQL 的批处理接口。

### 7.3 指针字段与稀疏数据

```go
type SparseMetrics struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	DevicePath  string    `gorm:"column:device_path" iotdb:"device"`
	Pressure    *float32  `gorm:"column:pressure"`
	Temperature *float64  `gorm:"column:temperature"`
	Online      *bool     `gorm:"column:online"`
}

rows := []SparseMetrics{
	{
		Time:       t0,
		DevicePath: "root.example.device_001.metrics_a",
		Pressure:   ptr(float32(1.20)),
		// Temperature 和 Online 为 nil：该时间点的两列写入 NULL。
	},
	{
		Time:        t1,
		DevicePath:  "root.example.device_001.metrics_a",
		Temperature: ptr(26.5),
		Online:      ptr(true),
		// Pressure 为 nil：该时间点的 pressure 写入 NULL。
	},
}

err := db.Table("logical_batch").CreateInBatches(&rows, 1000).Error
```

指针字段为 `nil` 时表示 NULL；非 nil 的 `false`、`0` 和空字符串仍是实际值。非指针字段的零值会作为实际值写入，不能用于表达 NULL。

同一批次中，同一 Device 的重复时间戳会在 RPC 发出前被拒绝。不同调用之间出现相同时间戳时，最终覆盖行为由 IoTDB 决定，调用方不能把 `RowsAffected` 当作幂等证明。

### 7.4 原生 SQL 写入

单 Device 的 IoTDB INSERT 可以通过 `db.Exec` 执行：

```go
err := db.Exec(
	"INSERT INTO root.example.device_001.metrics_a"+
		"(timestamp,pressure,temperature) VALUES (?,?,?)",
	t0,
	1.20,
	26.5,
).Error
```

驱动会绑定业务值，并按 `TimePrecision` 转换 `time.Time`。IoTDB 路径和 measurement 名不能放入参数占位符；动态标识符必须先经过白名单校验。

#### 不支持 TDengine 的多子表 INSERT 语法

TDengine 允许在一个 INSERT 字符串中连续描述多个目标表，例如：

```sql
INSERT INTO root.example.device_001.metrics_a
    (timestamp, pressure)
VALUES
    (1788888000000, 1.20)

root.example.device_002.metrics_a
    (timestamp, pressure)
VALUES
    (1788888000100, 1.30);
```

这不是 IoTDB 1.3.1 的 INSERT 语法。`db.Exec` 会把整个字符串作为一条 IoTDB SQL 发送，不会识别并拆分其中的 Device 片段。IoTDB 1.3.1 会返回语法错误码 700；在该版本的验证中，这次调用没有写入数据。

需要向多个 Device 写入时有两种支持方式：

1. 数据使用同一个结构体 Schema：使用带 `iotdb:"device"` 字段的 `CreateInBatches`，让驱动调用官方多 Tablet API；
2. 每个 Device 的 SQL 结构不同：为每个准确 Device 分别调用 `db.Exec`，逐次检查错误。多个 `Exec` 之间没有事务原子性，调用方需要记录成功范围并设计重试或补偿。

## 8. 数据查询

### 8.1 基础链式查询

```go
type QueryRow struct {
	Time        time.Time `gorm:"column:time"`
	Pressure    *float32  `gorm:"column:pressure"`
	Temperature *float64  `gorm:"column:temperature"`
}

var rows []QueryRow
err := db.Table("root.example.device_001.metrics_a").
	Select("pressure", "temperature").
	Where("time >= ? AND time < ?", begin, end).
	Where("pressure > ?", 1.0).
	Order("time DESC").
	Limit(100).
	Find(&rows).Error
```

| API | 状态 | 可用行为 | 约束 |
|---|---|---|---|
| `Table(path)` | 支持并已验证 | 固定 Device，或 TreeModel 只读 `*` / `**` 路径 | 别名和任意表表达式不支持；通配符不能写入或迁移 |
| `Select(...)` | 支持并已验证 | 选择 measurement，或写入 IoTDB 聚合表达式 | TreeModel 的 `time` 是服务端隐式结果列，不要在 `Select` 中重复列出 |
| `Where(...)` | 支持并已验证 | 参数绑定、多个条件叠加、时间和数值过滤 | 占位符只表示值，不能表示路径或 measurement |
| `Order(...)` | 支持并已验证 | `time ASC` 和 `time DESC` | 其他排序表达式尚未验证 |
| `Limit(...)` | 支持并已验证 | 限制返回行数 | 不能替代时间范围；通配符仍可能展开大量列 |
| `Offset(...)` | 支持 | 与 `Limit` 一起生成 IoTDB `OFFSET` | 尚未覆盖所有查询形状 |
| `Find(&[]Model{})` | 支持并已验证 | 固定 Device 结果映射到结构体，时间映射为 `time.Time` | 字段名或 `gorm:"column:..."` 必须与返回列对应 |
| `Find(&[]map[string]any{})` | 支持并已验证 | 接收通配符查询产生的动态完整路径列 | 调用方按完整路径读取值 |
| `Scan(&dst)` | 支持 | 扫描自定义结果结构 | 聚合和元数据查询应先核对返回列名及类型 |
| `Raw(sql, args...)` | 支持并已验证 | 执行 IoTDB 专用查询 | 绕过 `Table()` 路径校验；SQL 结构必须受控 |
| `Rows()` | 支持并已验证 | 流式读取动态结果 | 调用方必须调用 `Close()` |

参数占位符由驱动安全转换为 IoTDB 字面量，不是服务端 prepared statement。不要启用 GORM `PrepareStmt`；驱动明确不支持 `Prepare` 和 `PrepareContext`。

### 8.2 聚合函数

IoTDB 1.3.1 TreeModel 已验证以下聚合函数：

```go
var result []map[string]any
err := db.Table("root.example.device_001.metrics_a").
	Select(
		"count(pressure)",
		"sum(pressure)",
		"avg(pressure)",
		"min_value(pressure)",
		"max_value(pressure)",
		"first_value(pressure)",
		"last_value(pressure)",
	).
	Where("time >= ? AND time < ?", begin, end).
	Scan(&result).Error
```

`sum` 和 `avg` 只应用于兼容的数值 measurement。IoTDB 的 `count(*)` 会分别统计各 measurement 的非空点数，不等于关系型表的“记录行数”，因此不能把 GORM `Count()` 当作设备记录数查询。

### 8.3 固定时间窗口聚合

GORM `Group()` 的通用关系型语义尚未验证。需要 IoTDB 固定时间窗口时，应使用结构固定的 `Raw()`：

```go
var windows []map[string]any
err := db.Raw(
	"SELECT count(pressure), sum(pressure), avg(pressure) "+
		"FROM root.example.device_001.metrics_a "+
		"GROUP BY ([?, ?), 1000ms)",
	begin.UnixMilli(),
	end.UnixMilli(),
).Scan(&windows).Error
```

`[begin, end)` 表示左闭右开的查询区间，`1000ms` 是每个窗口的长度。窗口长度属于 SQL 结构，若由外部输入决定，调用方必须先解析为允许的时间单位和数值，不能直接拼接原始请求文本。

### 8.4 跨指标分组查询

同一业务设备下的不同指标分组是不同 IoTDB Device，`iotdb-gorm` 不支持用 GORM `Joins()` 把它们连接成关系型结果。推荐分别查询，再按时间戳在应用层合并：

```go
type PressureRow struct {
	Time     time.Time `gorm:"column:time"`
	Pressure *float32  `gorm:"column:pressure"`
}

type SpeedRow struct {
	Time          time.Time `gorm:"column:time"`
	RotationSpeed *float32  `gorm:"column:speed"`
}

businessDevice := "root.example.device_001"

var pressureRows []PressureRow
if err := db.Table(businessDevice+".metrics_a").
	Select("pressure").
	Where("time >= ? AND time < ?", begin, end).
	Order("time ASC").
	Find(&pressureRows).Error; err != nil {
	return err
}

var speedRows []SpeedRow
if err := db.Table(businessDevice+".metrics_b").
	Select("speed").
	Where("time >= ? AND time < ?", begin, end).
	Order("time ASC").
	Find(&speedRows).Error; err != nil {
	return err
}

timeline := mergeByTimestamp(pressureRows, speedRows)
```

`mergeByTimestamp` 是业务代码需要实现的合并函数，不属于 `iotdb-gorm`。合并逻辑必须明确时间戳精度、缺失点、NULL、重复点和时区策略。

如果只需要在一个查询中读取相同层级的同名 measurement，可以使用第 5.2 节的 `*` / `**` 通配符查询；它返回多条完整测点路径列，不会替代应用层的异构字段 Join。

## 9. TreeModel 元数据维护与删除

### 9.1 alias、Tag 和 Attribute

TreeModel timeseries 的 alias、Tag 和 Attribute 不属于 `AutoMigrate` 管理范围。驱动没有为它们提供专用 GORM API，需要通过 `db.Exec` 和 `db.Raw` 执行 IoTDB SQL。

以下示例中的 measurement 路径和元数据键由程序固定；元数据值通过参数绑定：

```go
path := "root.example.device_001.device_profile"

if err := db.Exec(
	"ALTER TIMESERIES "+path+" ADD TAGS region=?, building=?, temp_tag=?",
	"region-a", "building-1", "temporary",
).Error; err != nil {
	return err
}

if err := db.Exec(
	"ALTER TIMESERIES "+path+
		" ADD ATTRIBUTES display_name=?, description=?, temp_attr=?",
	"device-a", "example metadata", "temporary",
).Error; err != nil {
	return err
}

if err := db.Exec(
	"ALTER TIMESERIES "+path+" SET 'region'=?, 'display_name'=?",
	"region-b", "device-b",
).Error; err != nil {
	return err
}

if err := db.Exec(
	"ALTER TIMESERIES " + path + " RENAME temp_tag TO renamed_tag",
).Error; err != nil {
	return err
}

if err := db.Exec(
	"ALTER TIMESERIES " + path + " RENAME temp_attr TO renamed_attr",
).Error; err != nil {
	return err
}

if err := db.Exec(
	"ALTER TIMESERIES " + path + " DROP renamed_tag, renamed_attr",
).Error; err != nil {
	return err
}
```

上述 Tag 和 Attribute 的新增、修改、重命名、删除及查询语法已在 IoTDB 1.3.1 验证。`db.Exec` 对这类 DDL 返回的 `RowsAffected` 为 0，是否成功应以 `Error` 和后续元数据查询为准。

alias 使用 IoTDB 的 `UPSERT ALIAS` 语法。驱动可以提交该 SQL，但此语法没有纳入本文的 IoTDB 1.3.1 验证范围，接入前应在目标服务端版本独立验证：

```go
err := db.Exec(
	"ALTER TIMESERIES root.example.device_001.metrics_a.pressure "+
		"UPSERT ALIAS=pressure_alias",
).Error
```

查询完整元数据或按 Tag 筛选：

```go
rows, err := db.Raw("SHOW TIMESERIES " + path).Rows()
if err != nil {
	return err
}
defer rows.Close()

matched, err := db.Raw(
	"SHOW TIMESERIES "+path+" WHERE TAGS(region) = ?",
	"region-b",
).Rows()
if err != nil {
	return err
}
defer matched.Close()
```

路径、Tag 键、Attribute 键和 alias 都是 SQL 标识符，不能用 `?` 绑定。动态标识符必须来自白名单；不能把 HTTP、MQTT 或用户输入直接拼接进 SQL。

### 9.2 删除时间范围内的数据

GORM `Delete()` 会被驱动拒绝。删除时序数据必须显式指定 IoTDB 路径和时间范围：

```go
device := "root.example.cleanup_device.cleanup"

// isAllowedCleanupDevice 是应用提供的删除白名单，不属于 iotdb-gorm。
if !isAllowedCleanupDevice(device) {
	return errors.New("device is outside cleanup scope")
}

if err := db.Exec(
	"DELETE FROM "+device+".* WHERE time >= ? AND time < ?",
	begin.UnixMilli(),
	end.UnixMilli(),
).Error; err != nil {
	return err
}
```

删除前后都应执行范围查询，确认删除数量和保留数据符合预期。多个删除语句之间没有事务回滚能力。

### 9.3 删除 measurement

```go
paths := []string{
	"root.example.cleanup_device.cleanup.tmp_delete_01",
	"root.example.cleanup_device.cleanup.tmp_delete_02",
}
for _, path := range paths {
	if !isAllowedCleanupMeasurement(path) {
		return errors.New("measurement is outside cleanup scope")
	}
}

if err := db.Exec("DELETE TIMESERIES " + strings.Join(paths, ",")).Error; err != nil {
	return err
}
```

推荐只删除专用 cleanup Device 下、带运行批次标识的准确 measurement。删除步骤至少包括：删除前 `SHOW TIMESERIES`、精确白名单校验、执行删除、删除后再次查询、确认业务保留路径未变化。

## 10. 明确不支持和尚未验证的 GORM API

### 10.1 查询和写入

| API 或语义 | 状态 | 原因 | 替代方式 |
|---|---|---|---|
| 默认事务、`Transaction()`、`Begin/Commit/Rollback` | 不支持 | IoTDB 没有可安全映射的关系型事务语义 | 配置 `SkipDefaultTransaction: true`；按单次 RPC 设计幂等、重试和补偿 |
| `SavePoint()`、`RollbackTo()` | 不支持 | IoTDB 没有保存点语义 | 拆分操作并记录已完成范围 |
| `Prepare()`、`PrepareContext()`、GORM `PrepareStmt` | 不支持 | 驱动使用本地参数绑定，不创建服务端 prepared statement | 保持 `PrepareStmt: false`，继续使用 `?` 绑定业务值 |
| `Update()`、`Updates()`、`Save()` | 不支持 | 关系型行更新无法安全映射为时序写入 | 用 `Create` / `CreateInBatches` 写入新的时间点 |
| GORM `Delete()` | 不支持 | 自动生成的关系型删除语义不足以保护时序路径 | 使用第 9 章的受控 IoTDB SQL |
| `Joins()` 和 SQL JOIN | 不支持 | TreeModel Device 不是可连接的关系表；驱动会在构造查询时拒绝 | 分别查询，在应用层按时间戳或业务键合并 |
| `Preload()`、`Association()` | 不支持 | IoTDB 没有 GORM 关系、外键和关联预加载语义 | 在应用服务层显式加载关联数据 |
| `Count()` 作为关系表行数 | 不支持等价语义 | IoTDB `count(*)` 按 measurement 统计非空点 | 明确写出 `count(measurement)` 并按返回列解释结果 |
| `First()`、`Last()`、`Take()`、`Pluck()` | 尚未验证 | 隐式排序、限制和返回列形状可能不符合 IoTDB 语义 | 使用 `Order/Limit/Find` 或明确的 IoTDB 聚合函数 |
| `Distinct()`、`Group()`、`Having()` | 尚未验证 | GORM 生成的通用 SQL 未确认适配目标 IoTDB 版本 | 使用结构固定的 `Raw()`，并为目标 SQL 增加集成测试 |

### 10.2 Schema 迁移

| API | 状态 | 原因 | 替代方式 |
|---|---|---|---|
| `DropTable()`、`RenameTable()` | 不支持 | 驱动阻止自动破坏 Device 或表 | 编写独立迁移和回滚方案，人工审核 IoTDB DDL |
| `DropColumn()`、`AlterColumn()`、`RenameColumn()` | 不支持 | 类型和路径变更涉及历史时序数据 | 新建 measurement、迁移数据、切换读取后再清理旧路径 |
| `CreateView()`、`DropView()` | 不支持 | 未映射 GORM View 语义 | 使用目标 IoTDB 版本明确支持的原生能力 |
| `CreateConstraint()`、`DropConstraint()` | 不支持 | IoTDB 没有关系型外键和约束语义 | 在应用层校验 |
| `CreateIndex()`、`DropIndex()`、`RenameIndex()` | 不支持 | 不映射关系型普通索引或唯一索引 | 按 IoTDB 查询模型和官方能力设计 |

## 11. 官方客户端封装边界

### 11.1 驱动暴露的是 GORM 子集

`iotdb-gorm` 是官方 Go 客户端之上的适配层，不是官方客户端全部 API 的一一封装。业务代码通过 GORM 操作，不会直接获得官方 `Session`、`Tablet` 或 `SessionDataSet`，也不能调用每一种官方 Getter。

TreeModel 的 `Create` 和 `CreateInBatches` 会调用以下官方 Tablet API：

- 单个对齐 Tablet：`InsertAlignedTablet`；
- 多个对齐 Tablet：`InsertAlignedTablets`；
- 单个非对齐 Tablet：`InsertTablet`；
- 多个非对齐 Tablet：`InsertTablets`。

驱动不会把 `CreateInBatches` 转换为 `InsertAlignedRecords`。

### 11.2 批量模型写入不等于批量执行任意 SQL

`CreateInBatches` 接收同一种 Go 结构体的多行数据。驱动可以把这些行按 Device 分组，并通过一次官方多 Tablet 调用写入多个 TreeModel Device。这适合“字段结构一致、目标 Device 不同”的采集数据。

驱动没有提供“传入多条结构不同的 SQL，再用一次 RPC 执行”的 API。`db.Exec` 每次只发送一条 IoTDB SQL，也只返回这次调用的整体错误；它不会：

- 把一个字符串拆成多条 SQL 或多个 Device 片段；
- 为字符串中的每个片段分别返回成功或失败结果；
- 在多次 `Exec` 之间提供事务回滚。

因此，多个 Device 且 Schema 相同时使用 `CreateInBatches`；SQL 结构不同时分别调用 `db.Exec`，并由应用记录每次调用的结果。第 7.4 节展示的 TDengine 多子表 INSERT 不能作为 IoTDB 批量写入语法。

### 11.3 SQL 回退的安全边界

`Raw` 和 `Exec` 是执行 IoTDB 专用 SQL 的必要入口，同时也会绕过部分 GORM 语义和 `Table()` 路径保护。使用时遵循以下规则：

1. SQL 结构和关键字由程序固定；
2. 路径、measurement、Tag 键等标识符只从白名单构造；
3. 时间、数值和字符串等业务值使用 `?` 参数绑定；
4. 查询结果按 IoTDB 实际列名和类型接收；
5. 破坏性 SQL 在执行前后查询确认，并保留操作范围记录。

## 12. 上线前仍需完成的验证

本文确认的是 API 和指定数据库版本之间的功能边界，不包含以下生产验收：

- 多节点故障切换、重连顺序和恢复时间；
- 查询中断后的资源释放和重试策略；
- 写入超时或连接断开时的幂等、重复点和补偿策略；
- 长时间稳定性、连接泄漏、并发和容量压测；
- 历史数据迁移、备份恢复、TTL 和磁盘容量策略；
- 生产部署、监控告警和现场业务验收。

接入新 IoTDB 版本时，应至少回归连接、重复 `AutoMigrate`、单条与批量写入、写后查询、通配符列形状、聚合结果、元数据 SQL、删除安全边界以及驱动明确拒绝的 API。任何 IoTDB 2.0.x TableModel 生产接入——包括已经完成基础链路验证的 2.0.8——都需要补充目标业务使用到的 API、并发、故障切换和长时间稳定性验证。只有完成目标部署环境的功能、故障和容量验证后，才能把该环境标记为生产可用。

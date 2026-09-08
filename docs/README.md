# iotdb-gorm 使用指南

`iotdb-gorm` 是 Apache 官方 [`iotdb-client-go`](https://github.com/apache/iotdb-client-go) 之上的 GORM 适配层。业务项目通过 GORM API 使用 IoTDB；适配层负责连接池、模型映射、Tablet 批量写入和结果集转换。

## 兼容范围

| IoTDB 服务端 | 模型 | 官方客户端 | 验证状态 |
|---|---|---|---|
| 1.3.1 | TreeModel | 查询使用 `iotdb-client-go v1.3.7`；写入使用 `iotdb-client-go/v2 v2.0.8` | 已实机验证 |
| 2.0.8 | TableModel | `iotdb-client-go/v2 v2.0.8` | 已实机验证 |
| 2.0.10 | TableModel | 尚未确定 | 待独立实机验证 |

项目基线为 Go `1.23.2` 和 GORM `v1.23.4`。应用只需要直接引入 `iotdb-gorm` 和业务代码使用的 GORM 包，不需要直接导入 Apache 官方客户端；Go Modules 会自动解析适配层所需的官方客户端版本。

## 安装

正式项目应固定发布标签：

```bash
go get github.com/HY-805/iotdb-gorm@<固定版本标签>
```

适配层尚未发布标签时，可在调用项目的 `go.mod` 中临时指向本地目录：

```go
require github.com/HY-805/iotdb-gorm v0.0.0

replace github.com/HY-805/iotdb-gorm => ../iotdb-gorm
```

本地 `replace` 只用于开发和联调，发布或 CI 构建前应改为固定版本标签。

## TreeModel：IoTDB 1.3.1

TreeModel 是默认模式。`Database` 必须是受控根路径，例如 `root.datacenter_compatible`；`db.Table("device001")` 会映射为 `root.datacenter_compatible.device001`。

### 连接和模型

```go
package main

import (
	"context"
	"log"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type Telemetry struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	Temperature float64   `gorm:"column:temperature"`
	Online      bool      `gorm:"column:online"`
	Note        *string   `gorm:"column:note"`
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
	defer func() {
		if err := gormiotdb.Close(db); err != nil {
			log.Printf("close iotdb: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := gormiotdb.Ping(ctx, db); err != nil {
		log.Fatal(err)
	}

	device := db.WithContext(ctx).Table("device001")
	if err := device.AutoMigrate(&Telemetry{}); err != nil {
		log.Fatal(err)
	}
	if err := device.Create(&Telemetry{
		Time:        time.Now(),
		Temperature: 23.5,
		Online:      true,
	}).Error; err != nil {
		log.Fatal(err)
	}
}
```

必须设置 `SkipDefaultTransaction: true`。IoTDB 不提供与 GORM 默认事务回调等价的关系型事务语义。

`AutoMigrate` 是非破坏性的：它会创建缺失 measurement，并校验已有设备的对齐方式和 measurement 类型；可对同一设备重复执行。它不会删除、改名或修改已有类型。

### 查询

```go
var rows []Telemetry
err := db.WithContext(ctx).
	Table("device001").
	Where("time >= ? AND time < ?", begin, end).
	Order("time ASC").
	Limit(1000).
	Find(&rows).Error
```

TreeModel 的时间戳是服务端隐式结果列，不要在 `Select(...)` 中显式包含 `time`。`count(*)` 返回各 measurement 的聚合结果，不等价于关系表行数。IoTDB 1.3.1 对全 NULL TEXT 的返回无法可靠区分空字符串和 NULL，适配层会按 `nil` 处理。

### 多设备批量写入

使用 `iotdb:"device"` 指定每行目标设备。该字段只参与路由，不会写为 measurement：

```go
type BatchTelemetry struct {
	Time       time.Time `gorm:"column:time" iotdb:"time"`
	DevicePath string    `gorm:"column:device_path" iotdb:"device"`
	Value      float64   `gorm:"column:value"`
}

rows := []BatchTelemetry{
	{Time: time.Now(), DevicePath: "device_cn_01", Value: 20.5},
	{Time: time.Now(), DevicePath: "device_cn_02", Value: 21.3},
}
if err := db.Table("logical_batch").CreateInBatches(&rows, 1000).Error; err != nil {
	return err
}
```

适配层会按设备分组、按时间排序，并使用官方 `InsertAlignedTablets` 或 `InsertTablets`。同一设备内不允许重复时间戳；需要非对齐写入时配置 `Aligned: gormiotdb.Bool(false)`。

## TableModel：IoTDB 2.0.8

TableModel 必须显式设置 `ModelMode`。TAG、ATTRIBUTE 和 FIELD 也必须显式标记：

```go
type TableTelemetry struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	Region      string    `gorm:"column:region" iotdb:"tag"`
	DeviceID    string    `gorm:"column:device_id" iotdb:"tag"`
	Description string    `gorm:"column:description" iotdb:"attribute"`
	Temperature float64   `gorm:"column:temperature" iotdb:"field"`
	Note        *string   `gorm:"column:note" iotdb:"field"`
}

db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
	ModelMode: gormiotdb.TableModel,
	NodeURLs:  []string{"127.0.0.1:6667"},
	Username:  "root",
	Password:  "root",
	Database:  "iotdb_compatible",
}), &gorm.Config{SkipDefaultTransaction: true})
if err != nil {
	return err
}
defer gormiotdb.Close(db)

telemetry := db.Table("telemetry")
if err := telemetry.AutoMigrate(&TableTelemetry{}); err != nil {
	return err
}

rows := []TableTelemetry{
	{Time: time.Now(), Region: "cn", DeviceID: "d1", Temperature: 20.1},
	{Time: time.Now().Add(time.Millisecond), Region: "cn", DeviceID: "d1", Temperature: 20.2},
}
if err := telemetry.CreateInBatches(&rows, 1000).Error; err != nil {
	return err
}
```

TableModel 写入使用官方 `NewRelationalTablet` 和 `TableSessionPool.Insert`。`AutoMigrate` 会幂等创建缺失数据库、表和兼容的新列，运行账号需要相应 DDL 权限。

## Raw SQL

IoTDB 专用语句可以通过 `Raw` 或 `Exec` 执行。完整 Raw 查询不需要设置占位 `Table`：

```go
rows, err := db.Raw("SHOW STORAGE GROUP").Rows()
if err != nil {
	return err
}
defer rows.Close()

if err := db.Exec("FLUSH").Error; err != nil {
	return err
}
```

Raw SQL 的结构应由程序固定；业务值使用参数占位符，不要拼接不可信输入。普通 `Table(...).Find(...)` 仍会校验 TreeModel 设备路径，完整路径必须位于配置的 `Database` 根路径内。

## DSN 和配置

生产环境推荐使用结构化 `Config`，避免密码进入日志或命令行。DSN 适合本地快速验证：

```go
db, err := gorm.Open(gormiotdb.Open(
	"iotdb://root:password@127.0.0.1:6667/root.datacenter_compatible?model=tree&aligned=true&batch_size=1000",
), &gorm.Config{SkipDefaultTransaction: true})
```

| 配置 | 默认值 | 说明 |
|---|---:|---|
| `ModelMode` | `TreeModel` | `TreeModel` 或 `TableModel` |
| `NodeURLs` | 无 | 官方客户端节点列表，至少一个 `host:port` |
| `Database` | 无 | TreeModel 根路径或 TableModel database |
| `Aligned` | `true` | TreeModel 是否对齐写入 |
| `PoolSize` | `8` | 官方 SessionPool 最大 Session 数 |
| `ConnectTimeout` | `10s` | 建立连接超时 |
| `AcquireTimeout` | `10s` | 获取 Session 超时 |
| `QueryTimeout` | `30s` | 查询超时 |
| `BatchSize` | `1000` | 单个 Tablet 最大行数 |
| `MaxBatchBytes` | `4 MiB` | 单个 Tablet 估算字节上限 |
| `TableInsertConcurrency` | `4` | TableModel 多 Tablet 最大并发数 |
| `TimePrecision` | `Milliseconds` | `time.Time` 转换精度 |
| `DevicePathField` | 空 | 按字段名配置 TreeModel 设备路径 |
| `AllowSQLFallback` | `false` | 自定义连接是否允许 SQL INSERT 回退 |

`WithContext` 可以阻止尚未开始的操作并限制查询超时，但官方客户端内部的部分 RPC 不能保证在发出后立即取消。

## GORM 能力边界

当前支持：

- `Table`、`Select`、`Where`、`Order`、`Limit`
- `Find`、`Scan`、`Raw`、`Exec`、`WithContext`
- `Create`、`CreateInBatches`
- TreeModel 和 TableModel 的非破坏性 `AutoMigrate`

明确不支持：

- 默认事务、手工事务、SavePoint、RollbackTo
- `Updates`、`Save` 和 GORM `Delete`
- Association、Preload、Join、外键和唯一索引
- 自动 Drop、Rename 和类型修改
- 将 TDengine STABLE、子表、TAG 或 `last_row` 自动转换为通用 GORM 语义

删除数据应使用经过审查的 IoTDB SQL。不要使用 GORM `Delete`，也不要把不可信业务输入拼接进设备路径或 SQL。

## 测试

本地测试不访问远端 IoTDB：

```bash
go test ./...
go test -race ./...
go vet ./...
```

IoTDB 1.3.1 TreeModel 实机测试：

```bash
IOTDB_TREE_INTEGRATION=1 \
IOTDB_TREE_NODE_URL=127.0.0.1:6667 \
IOTDB_TREE_DATABASE=root.datacenter_compatible \
IOTDB_TREE_SECOND_DATABASE=root.systemcenter_compatible \
go test ./tests -run '^TestTreeIntegration' -count=1 -v
```

IoTDB 2.0.8 TableModel 实机测试：

```bash
IOTDB_TABLE_INTEGRATION=1 \
IOTDB_TABLE_NODE_URL=127.0.0.1:6667 \
IOTDB_TABLE_DATABASE=iotdb_test \
go test ./tests -run '^TestTableIntegration' -count=1 -v
```

实机测试使用唯一后缀的设备路径或表，并只清理本轮创建的数据。未设置集成测试开关时测试会显示 `SKIP`，不能视为实机验证通过。

# iotdb-gorm 设计说明

## 目标

`iotdb-gorm` 只负责把常用 GORM 调用转换到 Apache 官方 Go 客户端，业务层尽量不直接依赖 IoTDB 原生 Session API。

```text
GORM
  -> Dialector / callbacks / schema mapping
      -> database/sql bridge
          -> official TreeModel/TableModel pools
              -> IoTDB
```

查询内部仍然需要生成 IoTDB SQL，因为官方客户端的查询入口是 SQL；批量写入不经过 SQL `INSERT`，而是直接进入官方 Tablet API。

## 生命周期

`Dialector.Initialize` 创建模型所需的官方池，再通过 `sql.OpenDB` 创建轻量 bridge。database/sql 的逻辑连接不会再创建 IoTDB SessionPool。应用退出时调用 `gormiotdb.Close(db)`，该函数先关闭 bridge，再等待在途官方结果集释放，最后关闭官方池。

一个 Dialector 不应被多个独立 GORM 实例重复初始化；平台项目通过 Wire 创建一次 `*gorm.DB` 并复用。

## TreeModel

TreeModel 是默认模式，面向 IoTDB 1.3.1：

- `Database` 必须是 `root.xxx` 根路径；
- `db.Table("device001")` 解析为 `Database.device001`；
- `Time` 字段写入 Tablet 时间列；
- 其他普通字段写入 measurement；
- `Aligned` 默认 true；
- `iotdb:"device"` 字段或 `DevicePathFunc` 可显式提供每行设备路径；
- `iotdb:"tag"` 和 `iotdb:"attribute"` 不会被隐式转换，TreeModel 下直接报错。

TreeModel 的 Tablet 写入和 schema API 使用官方 v2.0.8，结果集读取使用与 IoTDB 1.3.1 匹配的官方 v1.3.7。时间戳是查询结果的隐式列，`Select` 不应显式包含 `time`；`count(*)` 也不是设备记录行数。服务端会把全 NULL TEXT 返回为空 Binary，适配层按 `nil` 返回，因而空字符串与 NULL 不可区分。

建模调用使用官方 `CreateAlignedTimeseries`、`CreateMultiTimeseries` 等接口。Migrator 只创建缺失测点和校验类型，不执行 Drop、Rename 或类型修改。

## TableModel

TableModel 面向 IoTDB 2.0.10：

- `Database` 是表模型 database 名称；
- `iotdb:"tag"` 映射为 TAG；
- `iotdb:"attribute"` 映射为 ATTRIBUTE；
- `iotdb:"field"` 或普通字段映射为 FIELD；
- `Time` 使用关系表隐式时间列；
- 写入使用官方 `NewRelationalTablet` 和 `ITableSession.Insert`。

数据库、表和新增列使用 IoTDB 表模型 DDL；迁移只允许幂等创建和兼容性加列。

## 批量写入

Create callback 的核心步骤如下：

1. 读取 GORM schema、Select/Omit 结果和时间字段；
2. 解析设备路径或关系表名；
3. 按设备/表分组；
4. 转换 BOOLEAN、INT32、INT64、FLOAT、DOUBLE、TEXT、BLOB、TIMESTAMP、DATE；
5. NULL 通过官方 Tablet bitmap 表示；
6. 按时间排序并拒绝同一目标内的重复时间戳；
7. 依据行数和估算字节数切分 Tablet；
8. 调用官方单 Tablet 或多 Tablet API。

当前默认批大小和字节上限是保护性默认值，平台应通过基准测试按实际字段数量调整。

## 参数和查询

官方客户端的 SQL 执行入口不提供本适配层所需的通用 database/sql 参数绑定，因此 bridge 使用状态机把值编码为 SQL 字面量：

- 只替换引号、反引号和注释之外的 `?`；
- 字符串使用 SQL 单引号转义；
- 数值拒绝 NaN/Inf；
- `time.Time` 按配置精度转为 Unix 时间；
- 动态表名和 Tree 路径只允许经过内部标识符校验。

该机制不能把业务输入当作表名或 SQL 片段。需要复杂 IoTDB 专用语句时使用 `Raw/Exec`，并由调用方负责固定 SQL 结构。

## 不支持能力

IoTDB Tree/Table 两种模型都不是完整关系数据库。适配层明确拒绝默认事务、Updates、Save、Delete、Preload、Join、关联、外键、索引、SavePoint 和破坏性迁移；不使用空实现掩盖语义差异。

## Context 限制

适配层在 Session 获取、执行前后和结果读取时检查 Context，并向查询 API 传递超时。官方 v1/v2 中部分底层方法仍使用 `context.Background()`，所以不能把 `WithContext` 描述为可中断所有已经发出的 RPC。

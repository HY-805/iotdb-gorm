# 上游来源说明

## 固定来源

- 上游仓库：`https://github.com/wkk778/gorm-iotdb`
- 导入提交：`757387622937a20c1c74e36ef0b0ab51ae2807c9`
- 导入方式：固定源码快照，不使用运行时 fork 依赖
- 许可证：Apache License 2.0，见仓库根目录 `LICENSE`

本仓库保留 `upstream` Git remote 仅用于查看差异。后续更新必须按提交手工评估，不自动合并，不在平台项目中直接引用上游 `main`。

## 复用内容

初始快照提供了以下可复用基础：

- GORM Dialector 的基本接口形态；
- GORM schema 与 IoTDB 类型映射思路；
- `database/sql` 结果扫描入口；
- 示例、CI 和文档目录结构。

## 已重写内容

本仓库针对平台约束重写了核心执行路径：

- module path 改为 `github.com/HY-805/iotdb-gorm`；
- Go 固定 `1.23.2`，GORM 固定 `v1.26.1`；
- IoTDB 1.3.1 TreeModel 查询固定 `iotdb-client-go v1.3.7`，Tablet 写入和 TableModel 固定 `iotdb-client-go/v2 v2.0.8`；
- 增加默认 TreeModel 与显式 TableModel；
- 移除“每个 database/sql 连接内部再创建 SessionPool”的嵌套池；
- 移除伪事务、伪 Commit、Rollback 关闭连接等行为；
- `Create/CreateInBatches` 重写为官方 Tablet/RelationalTablet API；
- TreeMigrator 改用官方时间序列 schema API，不生成 `CREATE TABLE`；
- TableMigrator 改为 TableModel DDL，并限制为非破坏性迁移；
- 重写参数字面量编码、NULL/INT64/TIMESTAMP/BLOB 扫描和结果集关闭；
- 增加路径根边界、批次边界、并发上限和显式不支持错误；
- 增加 IoTDB 1.3.1 与 2.0.10 的独立真机集成测试入口。

## 官方客户端优先策略

本项目定位是官方客户端的 GORM 适配层，不复制 Apache RPC、Tablet 序列化、SessionPool 或重连实现。

2026-09-07 使用更新后的账号完成 IoTDB 1.3.1 真机测试。v2.0.8 查询客户端在全 NULL TEXT 列上发生结果块错位，而官方 v1.3.7 能稳定解码 1.3.1 TreeModel；因此按既定策略拆分为 v1 Tree 查询、v2 Tablet 写入和 v2 TableModel。两套均为 Apache 官方客户端，不引入自定义 RPC 实现。

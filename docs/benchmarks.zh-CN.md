# Tanvo 基准测试

> [English](benchmarks.md) | 简体中文

> 相关文档：[架构](architecture.zh-CN.md) · [开发指南](development.zh-CN.md) · [测试策略](testing.zh-CN.md) · [路线图](roadmap.zh-CN.md) · [AGENTS.zh-CN.md](../AGENTS.zh-CN.md)

支撑设计决策的测量结果，保留下来，以便在其前提变化时重新检验该决策。每一节说明问了什么、如何测量、发现了什么、测量未覆盖什么，以及如何重新运行。数字来自一台机器，各次运行之间有 ±30% 的波动；应把它们当作数量级与比例来看，而不是容量。

---

## 1. 任务队列：River 与 Redis 队列

测量于 2026-10-26，River v0.49.0。工具为 [`tools/queuebench`](../tools/queuebench)。

### 1.1 问题

River 这个 PostgreSQL 队列是否会限制后台任务（[架构](architecture.zh-CN.md#9-任务与调度libsjobs) §9）？基于 Redis 的队列会不会更好？Tanvo 把每个任务放在需要它的状态变更所在的事务中入队，因此保持这一保证的 Redis 队列需要 outbox。

### 1.2 方法

所有系统运行同一工作负载。生产者事务插入一行业务数据并为它入队一个任务；worker 运行该任务，任务什么也不做（`noop`），或更新这一行（`pg`，多一次 PostgreSQL 写入，Tanvo 的大多数任务都是如此）。任务的延迟从入队算到其工作结束。

| 系统 | 入队 | 保证 |
| :--- | :--- | :--- |
| `river` | 在生产者事务中调用 River `InsertTx`，与 Tanvo 的入队方式相同 | 事务性 |
| `stream` | 提交后 `XADD` 到 Valkey stream；消费者组以 `XREADGROUP` 与 `XACK` 消费 | 无：提交与 `XADD` 之间崩溃会丢失任务。是 Redis 的上限 |
| `outbox` | 在生产者事务中写一行 outbox；4 个中继每批 500 行搬到 stream（`DELETE … FOR UPDATE SKIP LOCKED RETURNING`，然后流水线 `XADD`） | 事务性，至少一次：保持 Tanvo 保证的 Redis 队列的代价 |

两种模式：`max` 用 32 个生产者尽快入队 50,000 个任务（River 默认值时为 30,000 个）；`rate` 以固定速率入队 20 秒。除非另有说明，50 个 worker、一个 River 队列、100 个连接的连接池。`cd` 是 River 的取任务冷却期（`FetchCooldown`）；`W` 为 worker 数，`P` 为生产者数。

环境：一台 MacBook（Apple 芯片，10 核），OrbStack 上的 Docker；`postgres:18`，`max_connections=300`、`shared_buffers=1GB`；`valkey/valkey:8.1.10`，开启 AOF，`appendfsync everysec`；基准程序在宿主机上单进程运行。

### 1.3 结果

什么也不做的任务（端到端速率与延迟）：

| 系统 | 任务/秒 | p50 | p99 |
| :--- | ---: | ---: | ---: |
| River，默认值（cd 100 ms，W 50） | 499 | 27.6 s | 54.7 s |
| River，cd 50 ms | 993 | 20.6 s | 41.1 s |
| River，cd 25 ms | 1,979 | 8.2 s | 16.2 s |
| River，cd 10 ms | 4,248 | 1.4 s | 2.7 s |
| River，cd 25 ms，W 200 | 4,572 | 26 ms | 85 ms |
| Stream | 3,541 | 1.3 ms | 3.7 ms |
| Outbox | 3,388 | 11.1 ms | 295 ms |

更新其业务行的任务：

| 系统 | 任务/秒 | p50 | p99 |
| :--- | ---: | ---: | ---: |
| River，默认值 | 488 | 27.5 s | 52.3 s |
| River，cd 10 ms | 2,032 | 5.1 s | 7.3 s |
| River，cd 25 ms，W 200 | 2,845 | 35 ms | 247 ms |
| Stream | 3,430 | 5.1 ms | 48 ms |
| Outbox | 3,498 | 17.3 ms | 377 ms |
| River，cd 25 ms，W 200，P 96 | 3,159 | 0.8 s | 1.2 s |
| Stream，P 96 | 3,730 | 1.7 s | 3.9 s |
| Outbox，P 96 | 3,271 | 3.1 s | 4.5 s |

固定速率下，更新其业务行的任务：

| 系统 | 500/秒：完成/秒 | p50 | p99 | 2,000/秒：完成/秒 | p50 | p99 |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| River，默认值 | 468 | 166 ms | 1.38 s | 482 | 32.8 s | 62.2 s |
| River，cd 25 ms（W 50） | 481 | 19 ms | 73 ms | 1,552 | 2.2 s | 5.5 s |
| Stream | 496 | 6 ms | 215 ms | 1,999 | 5.8 ms | 207 ms |
| Outbox | 500 | 5.7 ms | 53 ms | 1,954 | 23 ms | 430 ms |

每次运行的原始输出是 `queuebench` 打印的最后一行；`run.sh` 打印整个矩阵。

### 1.4 结论

* **River 的上限在于取任务，而不在 PostgreSQL。** River 每个冷却期最多取一次队列，取走与空闲 worker 数相同的任务，因此在一个进程中，一个队列每秒最多运行 worker 数 ÷ 冷却期个任务：50 ÷ 100 ms = 500，50 ÷ 50 ms = 1,000，50 ÷ 25 ms = 2,000，与测量一致。在一个冷却期内完成的任务会让其 worker 空闲到下一次取任务。
* **每次取任务有足够多的 worker 时，River 的吞吐与 Redis 队列相当。** 在 25 ms、200 个 worker 时，River 每秒运行 4,572 个空任务，高于 stream 的 3,541；带一次写入的任务为 2,845，stream 为 3,430。所有系统的上限都在每秒 3,000–4,500 个任务，这正是本机上入队的 PostgreSQL 事务（以及任务自身的写入）所能承受的：Redis 省去的是队列的 PostgreSQL 写入，而不是业务事务的写入。
* **低于 25 ms 的冷却期得不偿失。** 在 10 ms 时取任务与插入互相争用：对带写入的任务，50 个 worker 的 River 降到每秒 2,032 个，低于 25 ms、200 个 worker 时。
* **Redis 的优势在延迟。** 非事务性的 stream 在入队后约 5 ms 运行任务，而 25 ms 冷却期的 River 为 20–35 ms（平均半个冷却期，加上取任务的时间）。事务性的 outbox 又把这一优势还了回去：它的中继按批处理，尾部延迟（p99 50–430 ms）并不比 River 好。
* **对 Tanvo 的影响。** River 100 ms 的默认值把 billing 队列的每个分片（20 个 worker 分到 4 个分片，每片 5 个）限制在一个进程每秒 50 个任务，整个队列 200 个。Tanvo 的取任务冷却期现在是 25 ms（`TANVO_JOBS_FETCH_COOLDOWN`），`TANVO_JOBS_SHARD_CONCURRENCY` 可提高队列每个分片的 worker 数，而不改变其他分片的份额（[架构 §9](architecture.zh-CN.md#9-任务与调度libsjobs)）。不计划引入 Redis 队列：它会增加一个组件和一个 outbox，只为省下几十毫秒的延迟，而没有哪个计费任务需要这些。

### 1.5 未覆盖的内容

以下各项如果结果不佳，会改变上述结论；在依赖 River 承载远高于上述速率的负载之前，应先测量它们。

* **多个进程消费同一队列。** 每个进程各自取任务，因此它们的取任务会在 `river_job` 上争用；本基准只运行了一个进程。
* **长期的表膨胀。** 每次运行在刚清空的 `river_job` 上持续数秒到数分钟。没有运行数天、数百万任务、伴随 River 清理已完成任务与 autovacuum 的情形。
* **生产硬件。** 服务器、PostgreSQL 与 Valkey 之间的网络延迟，以及真实 `fsync` 开销的磁盘，都会降低这里的每个速率；系统之间的比例比速率本身更重要。
* **Tanvo 自己的任务。** 工作负载是一次插入和一次更新；一个计费任务会执行许多查询，因此决定其吞吐的是它自身的工作，而不是队列。

### 1.6 重新运行

`tools/queuebench/run.sh` 在 55432 与 56379 端口启动 PostgreSQL 与 Valkey 容器，构建工具并运行上述矩阵，每次运行一行；对已运行的容器，单次运行为 `go run ./tools/queuebench -system river -work pg -n 50000 -cooldown 25ms -workers 200`（`-h` 列出所有参数）。

每次运行开始时会先结束数据库的所有其他客户端连接。在端口转发（OrbStack、Docker Desktop）之后，前一次运行的连接可能比其进程活得更久，处于事务中空闲状态，持有锁和旧快照，拖慢之后的每次运行，直到转发将其断开；容器也把 `idle_in_transaction_session_timeout` 设为 10 秒。加入这一防护之前的运行把带写入任务的速度测低了三到六倍，未予报告。

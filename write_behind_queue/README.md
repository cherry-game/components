# write-behind-queue 组件

高性能异步批量写入组件,专为游戏服务器设计,支持批量写入优化和优雅关闭。

## 核心特性

- ✅ **异步批量写入** - 将频繁的小写入合并为大批量,减少数据库 IO
- ✅ **智能去重合并** - 相同 key 只保留最新值,大幅减少写入次数
- ✅ **多队列分片** - 按 PlayerID 哈希分流,避免热点竞争
- ✅ **定时+容量触发** - 批量大小或时间间隔任一到达即刷新
- ✅ **优雅关闭** - 保证进程退出时所有数据安全落盘
- ✅ **可插拔后端** - 支持 Redis/MongoDB/MySQL 等任意数据库

## Install

### Prerequisites
- GO >= 1.21

### Using go get
```bash
go get github.com/cherry-game/examples/demo_cluster/internal/component/write_behind_queue@latest

Quick Start
import writebehindqueue "github.com/cherry-game/examples/demo_cluster/internal/component/write_behind_queue"

package main

import (
    "time"

    "github.com/cherry-game/cherry"
    cherryRedis "github.com/cherry-game/components/redis"
    writebehindqueue "github.com/cherry-game/examples/demo_cluster/internal/component/write_behind_queue"
)

func main() {
    // defaultConfig := writebehindqueue.DefaultTableConfig()
    // 1. 配置各业务表的队列参数
    configs := map[string]writebehindqueue.TableConfig{
        "player_data": {
            QueueCount:    4,                // 该表开 4 个 worker 并发处理
            QueueSize:     20480,             // Channel 缓冲区大小
            BulkSize:      500,              // 每 500 条批量写入
            FlushInterval: 10 * time.Second,  // 或 5 秒强制刷新
        },
        "room_data": {
            QueueCount:    2,
            QueueSize:     1024,
            BulkSize:      50,
            FlushInterval: 20 * time.Second,
        },
    }

    // 2. 初始化 Redis Backend
    redisComp := cherryRedis.NewRedisCompent()
    redisComp.Init()  // 注意：需要手动初始化
    backend := writebehindqueue.NewRedisBackend(redisComp.GetDb())

    // 3. 注册组件
    wbQueue := writebehindqueue.NewDBWriteQueueComponent(configs, backend)
    cherry.RegisterComponent(wbQueue)

    // 4. 启动应用
    cherry.Run()
}

提交写入任务

// 获取组件实例
wbQueue := app.Find("db_write_queue").(*writebehindqueue.DBWriteQueueComponent)

// 提交任务
task := &writebehindqueue.DbWriteTask{
    Table:      "player_data",     // 目标表名
    ExtraKeyId: "room_123",        // 可选的额外 key（如 roomId）
    PlayerID:   10001,              // 用于分片的玩家 ID
    OpType:     writebehindqueue.OpUpdate,
    Data:       playerData,         // 数据对象（需深拷贝）
}

success := wbQueue.SubmitTask(task)
if !success {
    // 处理失败情况（队列满/组件已停止）
}

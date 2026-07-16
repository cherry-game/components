package writebehindqueue

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	clog "github.com/cherry-game/cherry/logger"
	"github.com/redis/go-redis/v9"
)

// redis 数据直接存hash
type RedisBackend struct {
	client redis.UniversalClient
}

func NewRedisBackend(client redis.UniversalClient) *RedisBackend {
	if client == nil {
		clog.Panicf("[RedisBackend] Redis client is required")
	}
	return &RedisBackend{client: client}
}

func (r *RedisBackend) Save(ctx context.Context, data *DbWriteTask) error {
	return r.BatchSave(ctx, []*DbWriteTask{data})
}

// BatchSave 批量保存数据
func (r *RedisBackend) BatchSave(ctx context.Context, tasks []*DbWriteTask) error {
	if len(tasks) == 0 {
		return nil
	}
	clog.Infof("dbqueue flush len: %d", len(tasks))
	// 1. 开启 Pipeline 管道，将这批任务的所有网络 I/O 合并为一次发送
	pipe := r.client.Pipeline()
	for _, task := range tasks {
		hashKey := r.getHashKey(task.Table)
		FieldKey := r.getField(strconv.FormatInt(int64(task.PlayerID), 10), task.ExtraKeyId)
		if task.OpType == OpDelete {
			pipe.HDel(ctx, hashKey, FieldKey)
		} else {
			// 2. 序列化数据（Redis Hash 字段只能存储 String/Binary）
			pipe.HSet(ctx, hashKey, FieldKey, task.Data)
		}
	}
	// 3. 一次性提交并执行管道内的所有命令
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline exec failed: %w", err)
	}

	return nil
}

// Load 加载单条数据
func (r *RedisBackend) Load(ctx context.Context, table, key, ExtraKeyId string) ([]byte, error) {
	if len(table) == 0 || len(key) == 0 {
		return nil, fmt.Errorf("redis batch save json marshal failed,hashKey : %s, filedKey: %s", table, key)
	}
	hashKey := r.getHashKey(table)
	fieldKey := r.getField(key, ExtraKeyId)
	return r.client.HGet(ctx, hashKey, fieldKey).Bytes()
}

func (r *RedisBackend) getField(key ...string) string {
	return strings.Join(key, ":")
}

// 加上tableName hashkey
func (r *RedisBackend) getHashKey(tableName string) string {
	return fmt.Sprint("player_data", ":", tableName)
}

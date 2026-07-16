package writebehindqueue

import "context"

// ==================== 数据库接口抽象 ====================

// PersistenceBackend 持久化后端接口,使用者实现（ Redis/Mongo/MySQL等）
type PersistenceBackend interface {
	// Save 保存
	Save(ctx context.Context, data *DbWriteTask) error

	// BatchSave 批量保存数据
	BatchSave(ctx context.Context, tasks []*DbWriteTask) error

	// Load 加载单条数据
	Load(ctx context.Context, table, key, ExtraKeyId string) ([]byte, error)
}

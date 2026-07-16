package writebehindqueue

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	cfacade "github.com/cherry-game/cherry/facade"
	clog "github.com/cherry-game/cherry/logger"
)

// OpType 数据库操作类型定义
type OpType int

const (
	OpInsert OpType = 1
	OpUpdate OpType = 2
	OpDelete OpType = 3
)

// 通用队列数据任务 DbWriteTask
type DbWriteTask struct {
	Table      string      // 目标表名
	ExtraKeyId string      // 额外的key 比如roomId
	PlayerID   int32       // 玩家Id
	OpType     OpType      // 操作类型
	Data       interface{} // 玩家数据深拷贝的
}

// BatchSaver 具体的数据库批量写入执行者（解耦 GORM/MongoDB 等）
type BatchSaver interface {
	SaveBatch(table string, tasks []*DbWriteTask) error
}
type TableConfig struct {
	QueueCount    int           // 该表开启的队列(Worker)数量,建议设为跟主机的物理 CPU 核心数相同,不过根据实际数据量
	QueueSize     int           // 单个队列的 Channel 缓冲大小,根据表数据的大小
	BulkSize      int           // 单次批量写入的最大条数（去重后）
	FlushInterval time.Duration // 强制刷入数据库的时间间隔,根据数据重要性
	StopBulkSize  int           // 停服的时候单次批量写入的最大条数（去重后）,为了快速同步
}

func DefaultTableConfig() *TableConfig {
	return &TableConfig{
		QueueCount:    2,
		QueueSize:     10240,
		BulkSize:      200,
		FlushInterval: 10 * time.Second,
		StopBulkSize:  2000,
	}
}

type DBWriteQueueComponent struct {
	cfacade.Component
	name       string
	config     map[string]TableConfig // 表配置
	Saver      PersistenceBackend     // 具体的底层数据库保存实现
	workers    map[string][]*worker   // 运行中的 workers: table -> []*worker
	wg         sync.WaitGroup
	ctx        context.Context
	cancelFunc context.CancelFunc
	running    bool
	mu         sync.RWMutex
}

func NewDBWriteQueueComponent(config map[string]TableConfig, saver PersistenceBackend) *DBWriteQueueComponent {
	ctx, cancel := context.WithCancel(context.Background())
	if saver == nil {
		clog.Panicf(fmt.Sprintf("[%s] Init failed: BatchSaver is nil", "db_write_queue"))
	}
	return &DBWriteQueueComponent{
		config:     config,
		workers:    make(map[string][]*worker),
		ctx:        ctx,
		cancelFunc: cancel,
		Saver:      saver,
	}
}

func (d *DBWriteQueueComponent) Name() string {
	return "db_write_queue"
}

func (d *DBWriteQueueComponent) Init() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return
	}

	d.running = true
	for table, tCfg := range d.config {
		for i := 0; i < tCfg.QueueCount; i++ {
			w := &worker{
				queue:         make(chan *DbWriteTask, tCfg.QueueSize),
				table:         table,
				bulkSize:      tCfg.BulkSize,
				stopBulkSize:  tCfg.StopBulkSize,
				flushInterval: tCfg.FlushInterval,
				batchMap:      make(map[string]*DbWriteTask),
			}
			d.workers[table] = append(d.workers[table], w)
			d.wg.Add(1)
			go func(w *worker) {
				defer d.wg.Done()
				clog.Infof("Init work")
				w.run(d.ctx, d.Saver)
			}(w)
		}
		clog.Infof("[%s] Init table [%s] with %d queues successfully", d.Name(), table, tCfg.QueueCount)
	}
	clog.Infof("[%s] component init successfully", d.Name())
}

func (d *DBWriteQueueComponent) OnStop() {
	d.mu.Lock()
	d.running = false
	d.mu.Unlock()
	clog.Infof("[%s] Stopping, closing channels and flushing tasks...", d.Name())
	// 1. 停止接受新任务并关闭所有 worker 的 channel
	d.mu.RLock()
	for _, workers := range d.workers {
		for _, w := range workers {
			w.bulkSize = w.stopBulkSize
			close(w.queue) // 关闭通道，通知 worker 消费完剩余积压
		}
	}
	d.mu.RUnlock()
	// 等待所有 worker 处理完积压的任务
	// case task, ok := <-w.queue:
	// if !ok
	// 一定不能先 d.cancelFunc(),再d.wg.Wait(),可能chan中的消息还没有处理接受完,
	d.wg.Wait()
	// 2. 通知 context 结束（如果是阻塞等待的 timer 也会被快速释放）
	d.cancelFunc()

	clog.Infof("[%s] Stopped, all tasks flushed to DB safely.", d.Name())
}

func (d *DBWriteQueueComponent) SubmitTask(task *DbWriteTask) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.running {
		clog.Warnf("[%s] SubmitTask failed: component is stopped. task: %+v", d.Name(), task)
		return false
	}
	if _, ok := d.workers[task.Table]; !ok {
		clog.Errorf("[%s] SubmitTask failed: table [%s] queue not registered", d.Name(), task.Table)
		return false
	}
	// 同一个玩家的数据必须放在同一个桶里
	workerIndex := task.PlayerID % int32(len(d.workers[task.Table]))
	select {
	case d.workers[task.Table][workerIndex].queue <- task:
		return true
	default:
		// 当队列爆满时的降级机制（生产环境可根据业务需求选择：阻塞/丢弃/落日志告警）,或者重启扩大队列
		clog.Errorf("[%s] Queue is full for table [%s], player_id: %d", d.Name(), task.Table, task.PlayerID)
		return false
	}
}

type worker struct {
	queue         chan *DbWriteTask
	table         string
	bulkSize      int
	stopBulkSize  int
	flushInterval time.Duration
	batchMap      map[string]*DbWriteTask // 消费端用来合并去重的内存 Map
}

func (w *worker) run(ctx context.Context, Saver PersistenceBackend) {
	timer := time.NewTimer(w.flushInterval)
	defer timer.Stop()

	for {
		select {
		case task, ok := <-w.queue:
			if !ok {
				w.flush(ctx, Saver)
				clog.Infof("dbqueue channel closed")
				return
			}
			key := w.getHashKey(w.table, strconv.FormatInt(int64(task.PlayerID), 10), task.ExtraKeyId)
			w.batchMap[key] = task
			clog.Infof("dbqueue1 worker batchMap len: %d bulkSize: %d", len(w.batchMap), w.bulkSize)
			if len(w.batchMap) >= w.bulkSize {
				clog.Infof("dbqueue2 worker batchMap len: %d", len(w.batchMap), w.bulkSize)
				w.flush(ctx, Saver)
			}
		case <-ctx.Done():
			// 强制刷入剩余数据
			w.flush(ctx, Saver)
			clog.Infof("dbqueue worker stoped")
			return
		case <-timer.C:
			// 定时刷入数据
			clog.Infof("time loop dbqueue worker batchMap len: %d", len(w.batchMap))
			w.flush(ctx, Saver)
			timer.Reset(w.flushInterval)
		}
	}
}

func (w *worker) flush(ctx context.Context, Saver PersistenceBackend) {
	if len(w.batchMap) == 0 {
		return
	}
	tasks := make([]*DbWriteTask, 0, len(w.batchMap))
	for _, task := range w.batchMap {
		tasks = append(tasks, task)
	}

	if len(tasks) > 0 {
		// 【并发安全保护】：如果传入的 ctx 已经被取消（比如系统收到了 hard-kill 信号等）
		// 则创建一个带超时时间的全新独立上下文，确保 Redis 的最后一次 Pipeline 能够成功执行
		writeCtx := ctx
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			writeCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}

		err := Saver.BatchSave(writeCtx, tasks)
		if err != nil {
			clog.Errorf("save batch failed. err = %v", err)
			return // 发生错误时直接返回，不要清空 map，留到下次 flush 重试
		}
	}

	// 清空map
	for k := range w.batchMap {
		delete(w.batchMap, k)
	}
}

func (w *worker) getHashKey(tableName string, key ...string) string {
	return strings.Join(append([]string{tableName}, key...), ":")
}

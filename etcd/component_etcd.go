// cherryETCD 基于 etcd 的分布式节点发现组件。
// 通过 etcd lease 机制注册节点，利用 watch 感知集群成员变更。
// 断线后自动重连，重连期间保留本地 settings 数据。
package cherryETCD

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	cfacade "github.com/cherry-game/cherry/facade"
	clog "github.com/cherry-game/cherry/logger"
	cdiscovery "github.com/cherry-game/cherry/net/discovery"
	cproto "github.com/cherry-game/cherry/net/proto"
	cprofile "github.com/cherry-game/cherry/profile"
	jsoniter "github.com/json-iterator/go"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/namespace"
)

var (
	keyPrefix         = "/cherry/node/"
	registerKeyFormat = keyPrefix + "%s"
)

const (
	defaultOpTimeout   = 5 // 秒
	defaultRetryWait   = 3 // 秒
	defaultDialTimeout = 1 // 秒
)

// Component etcd 节点发现组件
type (
	Component struct {
		cdiscovery.ComponentDefault
		etcdConfig
		thisMember *cproto.Member // 当前节点成员信息，settings 变更时先更新此对象再同步到 etcd
		stopped    chan struct{}  // runLoop 退出时关闭，OnStop 通过它等待清理完成
	}

	etcdConfig struct {
		prefix        string             // etcd namespace 前缀
		config        clientv3.Config    // etcd 客户端配置
		ttl           int64              // lease TTL（秒）
		opTimeout     time.Duration      // 操作超时
		retryWait     time.Duration      // 断线重试间隔
		cli           *clientv3.Client   // etcd 客户端（初始化时构建，生命周期内不复建）
		ctx           context.Context    // OnStop 停服信号上下文
		cancel        context.CancelFunc // context cancel
		sessionCtx    context.Context    // 单次 etcd session 的生命周期上下文
		sessionCancel context.CancelFunc // session cancel
		leaseID       atomic.Int64       // 当前活跃的 lease，session 建立后赋值，断开后清零
	}
)

func New() *Component {
	return &Component{}
}

func (n *Component) Mode() string {
	return "etcd"
}

// Load 初始化组件：加载配置 → 构建 etcd 客户端 → 启动 runLoop。
// etcd 客户端仅在此时构建一次，后续断线重连复用同一连接。
func (p *Component) Init() {
	p.ComponentDefault.InitFields()

	clusterConfig := cprofile.GetConfig("cluster").GetConfig(p.Mode())
	if clusterConfig.LastError() != nil {
		clog.Fatalf("etcd config not found. err = %v", clusterConfig.LastError())
		return
	}
	p.loadConfig(clusterConfig)

	if err := p.newClient(); err != nil {
		clog.Fatalf("etcd create client failed: %v", err)
		return
	}

	p.thisMember = &cproto.Member{
		NodeID:   p.App().NodeID(),
		NodeType: p.App().NodeType(),
		Address:  p.App().RpcAddress(),
		Settings: make(map[string]string),
	}

	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.stopped = make(chan struct{})
	go p.runLoop()
}

// UpdateSetting 更新单个 setting 并同步到 etcd
func (p *Component) UpdateSetting(key, value string) {
	p.thisMember.UpdateSetting(key, value)
	p.putMember(clientv3.LeaseID(p.leaseID.Load()))
}

// UpdateSettings 批量更新 settings 并同步到 etcd
func (p *Component) UpdateSettings(setting map[string]string) {
	p.thisMember.UpdateSettings(setting)
	p.putMember(clientv3.LeaseID(p.leaseID.Load()))
}

// OnStop 发送停服信号，等待 runLoop 完成清理
func (p *Component) OnStop() {
	if p.cancel != nil {
		p.cancel()
	}
	<-p.stopped
}

// runLoop 连接维护主循环，通过 select 统一管理所有事件：
//   - ctx.Done     → 退出
//   - ticker.C     → 定时检查并触发 session 建立/重连
//   - watchChan    → 处理集群成员 PUT/DELETE 事件
//   - keepAliveCh  → keepalive 心跳断开，置空 watchChan 等待重连
func (p *Component) runLoop() {
	defer close(p.stopped)
	defer p.cleanup()

	ticker := time.NewTicker(p.retryWait)
	defer ticker.Stop()

	var (
		watchChan   clientv3.WatchChan
		keepAliveCh <-chan *clientv3.LeaseKeepAliveResponse
		err         error
	)

	for {
		select {
		// 停服信号
		case <-p.ctx.Done():
			return
		case <-ticker.C: // 定时巡检：应用未就绪则跳过，无 session 则建立
			if !p.App().Running() {
				clog.Info("[etcd] Waiting for the application to change its running state.")
				continue
			}

			if watchChan == nil {
				if p.sessionCtx != nil {
					p.closeSession()
				}

				watchChan, keepAliveCh, err = p.establishSession()
				if err != nil {
					clog.Warnf("[etcd] establish session failed: %v", err)
				}
			}
		case rsp, ok := <-watchChan: // watch 事件：channel 关闭/出错则标记断开，正常事件处理 PUT/DELETE
			if !ok {
				clog.Warn("[etcd] watch closed, cancel session")
				p.sessionCancel()
				watchChan = nil
				continue
			}
			if err := rsp.Err(); err != nil {
				clog.Warnf("[etcd] watch error: %v, cancel session", err)
				p.sessionCancel()
				watchChan = nil
				continue
			}
			for _, ev := range rsp.Events {
				switch ev.Type {
				case mvccpb.PUT:
					p.addMember(ev.Kv.Value)
				case mvccpb.DELETE:
					p.removeMember(ev.Kv)
				}
			}

		// keepalive 心跳断开，标记 session 断开等待重连
		case _, ok := <-keepAliveCh:
			if !ok {
				clog.Warn("[etcd] keepalive closed, cancel session")
				p.sessionCancel()
				watchChan = nil
				continue
			}
		}
	}
}

// cleanup 进程退出清理：① 主动删除 etcd key → ② 取消 session → ③ 关闭客户端。
// Delete 在 sessionCancel 之前执行，确保 lease 存活期间 key 被即时移除。
func (p *Component) cleanup() {
	if p.cli != nil {
		key := fmt.Sprintf(registerKeyFormat, p.App().NodeID())
		ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
		p.cli.Delete(ctx, key)
		cancel()

		if p.sessionCancel != nil {
			p.sessionCancel()
		}

		p.cli.Close()
	}
}

// newClient 创建 etcd 客户端并挂载 namespace 前缀
func (p *Component) newClient() error {
	cli, err := clientv3.New(p.config)
	if err != nil {
		return err
	}
	cli.KV = namespace.NewKV(cli.KV, p.prefix)
	cli.Watcher = namespace.NewWatcher(cli.Watcher, p.prefix)
	cli.Lease = namespace.NewLease(cli.Lease, p.prefix)
	p.cli = cli
	return nil
}

// establishSession 建立一次 etcd session：申请 lease → 注册节点 → 拉取已有成员 → 返回 watchChan 和 keepAliveCh
func (p *Component) establishSession() (clientv3.WatchChan, <-chan *clientv3.LeaseKeepAliveResponse, error) {
	p.sessionCtx, p.sessionCancel = context.WithCancel(context.Background())

	leaseID, keepAliveCh, err := p.grantLease()
	if err != nil {
		p.sessionCancel()
		return nil, nil, fmt.Errorf("grantLease: %w", err)
	}

	p.leaseID.Store(int64(leaseID))

	if err := p.putMember(leaseID); err != nil {
		p.sessionCancel()
		return nil, nil, fmt.Errorf("register: %w", err)
	}

	clog.Infof("[etcd] session established! [endpoints = %v, leaseID = %d]", p.config.Endpoints, leaseID)

	// 拉取 etcd 中已有成员列表
	ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
	resp, err := p.cli.Get(ctx, keyPrefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		clog.Warnf("[etcd] get existing members failed: %v", err)
	} else {
		for _, ev := range resp.Kvs {
			p.addMember(ev.Value)
		}
	}

	return p.cli.Watch(p.sessionCtx, keyPrefix, clientv3.WithPrefix()), keepAliveCh, nil
}

// closeSession 会话断开时：清除 lease 记录 → 取消 session
func (p *Component) closeSession() {
	p.leaseID.Store(0)
	if p.sessionCancel != nil {
		p.sessionCancel()
	}
}

// grantLease 向 etcd 申请 lease，返回 leaseID 和 keepalive 心跳通道
func (p *Component) grantLease() (clientv3.LeaseID, <-chan *clientv3.LeaseKeepAliveResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
	resp, err := p.cli.Grant(ctx, p.ttl)
	cancel()
	if err != nil {
		return 0, nil, err
	}

	keepAliveCh, err := p.cli.KeepAlive(p.sessionCtx, resp.ID)
	if err != nil {
		return 0, nil, err
	}

	return resp.ID, keepAliveCh, nil
}

// putMember 将 thisMember 序列化后写入 etcd，绑定指定 lease
func (p *Component) putMember(leaseID clientv3.LeaseID) error {
	jsonString, err := jsoniter.MarshalToString(p.thisMember)
	if err != nil {
		return err
	}

	key := fmt.Sprintf(registerKeyFormat, p.thisMember.NodeID)
	ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
	_, err = p.cli.Put(ctx, key, jsonString, clientv3.WithLease(leaseID))
	cancel()
	return err
}

// addMember 将 watch 到的节点数据反序列化后加入本地成员表。
// 已存在则更新（用于 settings 变更场景），不存在则添加。
func (p *Component) addMember(data []byte) {
	member := &cproto.Member{}
	err := jsoniter.Unmarshal(data, member)
	if err != nil {
		return
	}

	if p.thisMember.NodeID == member.NodeID {
		return
	}

	if _, found := p.GetMember(member.NodeID); !found {
		p.AddMember(member)
	} else {
		p.UpdateMember(member)
	}
}

// removeMember 从 key 中提取 nodeID，移除本地成员缓存
func (p *Component) removeMember(kv *mvccpb.KeyValue) {
	key := string(kv.Key)
	nodeID := strings.ReplaceAll(key, keyPrefix, "")
	if nodeID == "" {
		clog.Warn("remove member nodeID is empty!")
		return
	}
	p.RemoveMember(nodeID)
}

// loadConfig 从 profile 配置中加载 etcd 连接参数
func (p *Component) loadConfig(config cfacade.ProfileJSON) {
	p.config = clientv3.Config{
		Logger: clog.DefaultLogger.Desugar(),
	}

	p.config.Endpoints = getEndPoints(config)
	p.config.DialTimeout = config.GetDuration("dial_timeout_second", defaultDialTimeout) * time.Second
	p.config.Username = config.GetString("user")
	p.config.Password = config.GetString("password")

	p.ttl = config.GetInt64("ttl", 3)
	p.prefix = config.GetString("prefix", "cherry")
	p.opTimeout = time.Duration(config.GetInt64("op_timeout", defaultOpTimeout)) * time.Second
	p.retryWait = time.Duration(config.GetInt64("retry_wait", defaultRetryWait)) * time.Second
}

// getEndPoints 解析逗号分隔的 etcd 端点列表
func getEndPoints(config jsoniter.Any) []string {
	str := strings.TrimSpace(config.Get("end_points").ToString())
	if str == "" {
		return nil
	}
	var endpoints []string
	for _, ep := range strings.Split(str, ",") {
		ep = strings.TrimSpace(ep)
		if ep != "" {
			endpoints = append(endpoints, ep)
		}
	}
	return endpoints
}

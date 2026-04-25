package cherryETCD

import (
	"context"
	"fmt"
	"strings"
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

// ETCD etcd方式发现服务
type ETCD struct {
	app cfacade.IApplication
	cdiscovery.DiscoveryDefault

	prefix    string
	config    clientv3.Config
	ttl       int64
	opTimeout time.Duration
	retryWait time.Duration

	cli     *clientv3.Client
	leaseID clientv3.LeaseID

	stopCh        chan struct{}
	sessionCancel context.CancelFunc
	sessionDoneCh <-chan struct{}
}

func New() *ETCD {
	return &ETCD{}
}

func (p *ETCD) Name() string {
	return "etcd"
}

func (p *ETCD) Load(app cfacade.IApplication) {
	p.DiscoveryDefault.PreInit()
	p.app = app
	p.ttl = 10
	p.stopCh = make(chan struct{})

	clusterConfig := cprofile.GetConfig("cluster").GetConfig(p.Name())
	if clusterConfig.LastError() != nil {
		clog.Fatalf("etcd config not found. err = %v", clusterConfig.LastError())
		return
	}

	p.loadConfig(clusterConfig)
	go p.runLoop()
}

func (p *ETCD) OnStop() {
	close(p.stopCh)
	if p.sessionCancel != nil {
		p.sessionCancel()
	}
	if p.cli != nil {
		key := fmt.Sprintf(registerKeyFormat, p.app.NodeID())
		ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
		p.cli.Delete(ctx, key)
		cancel()
		p.cli.Close()
	}
}

// 主循环：连接 → 注册 → watch → 断连 → 重连
// 永不主动退出，只有 stopCh 关闭才退出
func (p *ETCD) runLoop() {
	for {
		if err := p.establishSession(); err != nil {
			if p.isStopped() {
				return
			}
			p.waitRetry("[etcd] establish session failed: %v", err)
			continue
		}

		clog.Infof("[etcd] session established! [endpoints = %v, leaseID = %d]", p.config.Endpoints, p.leaseID)

		select {
		case <-p.sessionDoneCh:
			// session 断开，清理后重连
			p.cleanupSession()
		case <-p.stopCh:
			return
		}
	}
}

func (p *ETCD) establishSession() error {
	if err := p.initClient(); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	if err := p.getLeaseID(); err != nil {
		p.cli.Close()
		return fmt.Errorf("getLeaseID: %w", err)
	}

	if err := p.register(); err != nil {
		p.sessionCancel()
		p.cli.Close()
		return fmt.Errorf("register: %w", err)
	}

	p.watch()
	return nil
}

func (p *ETCD) cleanupSession() {
	if p.sessionCancel != nil {
		p.sessionCancel()
	}
	if p.cli != nil {
		p.cli.Close()
	}
	selfID := p.app.NodeID()
	for nodeID := range p.Map() {
		if nodeID != selfID {
			p.RemoveMember(nodeID)
		}
	}
}

func (p *ETCD) isStopped() bool {
	select {
	case <-p.stopCh:
		return true
	default:
		return false
	}
}

func (p *ETCD) waitRetry(format string, args ...any) {
	clog.Warnf(format, args...)
	select {
	case <-time.After(p.retryWait):
	case <-p.stopCh:
	}
}

// 原有逻辑：init / getLeaseID / register / watch
// 改动：返回 error + 加超时 + 断连时 cancel session
func (p *ETCD) initClient() error {
	cli, err := clientv3.New(p.config)
	if err != nil {
		return err
	}
	cli.KV = namespace.NewKV(cli.KV, p.prefix)
	cli.Watcher = namespace.NewWatcher(cli.Watcher, p.prefix)
	cli.Lease = namespace.NewLease(cli.Lease, p.prefix)
	p.cli = cli

	ctx, cancel := context.WithCancel(context.Background())
	p.sessionCancel = cancel
	p.sessionDoneCh = ctx.Done()

	return nil
}

func (p *ETCD) getLeaseID() error {
	ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
	resp, err := p.cli.Grant(ctx, p.ttl)
	cancel()
	if err != nil {
		return err
	}

	p.leaseID = resp.ID

	keepaliveChan, err := p.cli.KeepAlive(context.Background(), resp.ID)
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case _, ok := <-keepaliveChan:
				if !ok {
					clog.Warn("[etcd] keepalive closed, cancel session")
					p.sessionCancel()
					return
				}
			case <-p.stopCh:
				return
			}
		}
	}()

	return nil
}

func (p *ETCD) register() error {
	registerMember := &cproto.Member{
		NodeID:   p.app.NodeID(),
		NodeType: p.app.NodeType(),
		Address:  p.app.RpcAddress(),
		Settings: make(map[string]string),
	}

	jsonString, err := jsoniter.MarshalToString(registerMember)
	if err != nil {
		return err
	}

	key := fmt.Sprintf(registerKeyFormat, p.app.NodeID())
	ctx, cancel := context.WithTimeout(context.Background(), p.opTimeout)
	_, err = p.cli.Put(ctx, key, jsonString, clientv3.WithLease(p.leaseID))

	cancel()
	return err
}

func (p *ETCD) watch() {
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

	watchChan := p.cli.Watch(context.Background(), keyPrefix, clientv3.WithPrefix())
	go func() {
		for rsp := range watchChan {
			if err := rsp.Err(); err != nil {
				clog.Warnf("[etcd] watch error: %v, cancel session", err)
				p.sessionCancel()
				return
			}
			for _, ev := range rsp.Events {
				switch ev.Type {
				case mvccpb.PUT:
					p.addMember(ev.Kv.Value)
				case mvccpb.DELETE:
					p.removeMember(ev.Kv)
				}
			}
		}
		clog.Warn("[etcd] watch closed, cancel session")
		p.sessionCancel()
	}()
}

func (p *ETCD) addMember(data []byte) {
	member := &cproto.Member{}
	err := jsoniter.Unmarshal(data, member)
	if err != nil {
		return
	}

	if _, found := p.GetMember(member.NodeID); !found {
		p.AddMember(member)
	}
}

func (p *ETCD) removeMember(kv *mvccpb.KeyValue) {
	key := string(kv.Key)
	nodeID := strings.ReplaceAll(key, keyPrefix, "")
	if nodeID == "" {
		clog.Warn("remove member nodeID is empty!")
		return
	}
	p.RemoveMember(nodeID)
}

func (p *ETCD) loadConfig(config cfacade.ProfileJSON) {
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

func getEndPoints(config jsoniter.Any) []string {
	return strings.Split(config.Get("end_points").ToString(), ",")
}

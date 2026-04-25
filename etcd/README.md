# etcd组件
- 基于etcd实现发现服务和节点集群

## Install

### Prerequisites
- GO >= 1.17

### Using go get
```
go get github.com/cherry-game/components/etcd@latest
```


## Quick Start
```
import cherryETCD "github.com/cherry-game/components/etcd"
```


```
// 注册etcd组件到discovery
func main() {
    cherryDiscovery.RegisterDiscovery(cherryETCD.New())
}

// 配置profile文件
// 设置"cluster"->"discovery"->"mode"为"etcd"模式
// 设置"cluster"->"etcd"节点相关的参数

{
    "cluster": {
        "discovery": {
            "mode": "etcd",
        },
        "nats": {
        },
        "etcd": {
            "end_points": "dev.com:2379",
            "@end_points": "dev.com:2379,dev1.com:2379",
            "prefix" : "cherry",
            "ttl": 5,
            "dial_timeout_second": 3,
            "op_timeout": 5,
            "retry_wait": 3,
            "user": "",
            "password": ""
        }
    }
}

```

## 配置说明

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| end_points | string | - | etcd地址，多个用逗号分隔 |
| prefix | string | "cherry" | etcd key命名空间前缀 |
| ttl | int | 5 | 租约时间（秒） |
| dial_timeout_second | int | 3 | 连接超时时间（秒） |
| op_timeout | int | 5 | etcd操作超时时间（秒），Grant/Put/Get等操作的超时 |
| retry_wait | int | 3 | 重试等待时间（秒），etcd不可用时重试间隔 |
| user | string | "" | etcd用户名 |
| password | string | "" | etcd密码 |

## example
- 示例代码待补充

# gops组件
- 基于gops实现进程监控与诊断
- 支持查看Go运行时信息（goroutine、stack、memstats等）

## Install

### Prerequisites
- GO >= 1.18

### Using go get
```
go get github.com/cherry-game/components/gops@latest
```


## Quick Start
```
import cherryGops "github.com/cherry-game/components/gops"
```

```
package demo

import (
	"github.com/cherry-game/cherry"
	cherryGops "github.com/cherry-game/components/gops"
)

func RegisterComponent() {
	// 使用默认配置
	gops := cherryGops.New()

	// 或自定义配置
	gops = cherryGops.New(agent.Options{
		Addr:    "0.0.0.0:0",
		ShutdownCleanup: true,
	})

	cherry.RegisterComponent(gops)
}
```

## 使用方式

启动集成了gops组件的服务后，可通过 `gops` 命令行工具进行诊断：

```bash
# 安装gops命令行工具
go install github.com/google/gops@latest

# 查看正在运行的Go进程
gops

# 查看指定进程的goroutine信息
gops stack <PID>

# 查看指定进程的内存状态
gops memstats <PID>

# 查看指定进程的GC信息
gops gcstats <PID>
```

## example
- 示例代码待补充

# cherry框架的通用组件库

### [cron组件](./cron/)

- 基于`github.com/robfig/cron/v3`进行封装成组件
- 性能良好

### [data-config组件](./data-config/)

- 策划配表读取管理组件
- 可基于本地配置文件的方式加载
- 可基于redis数据的方式加载
- 可基于接口抽像自定义数据源加载
- 支持自定义文件格式读取，目前已实现`JSON`格式读取
- 支持缓存热更新
- 可自定义类型检测
- 可根据`go-linq`进行数据集合的条件查询

### [etcd组件](./etcd/)

- 基于`etcd`组件进行封装，节点集群和注册发现

### [gin组件](./gin/)

- 集成`gin`组件，实现http server功能
- 自定义`controller`，增加`PreInit()`、`Init()`、`Stop()`初始周期的管理
- 增加几个常用的`middleware`组件
  - gin zap
  - recover with zap
  - cors跨域
  - max connect限流
- 封装了GET/POST方式获取各种数据类型的函数

### [gops组件](./gops/)

- gops用于线上获取go进程运行信息的组件

### [gorm组件](./gorm/)

- 集成`gorm`组件，实现mysql的数据库访问
- 支持多个mysql数据库配置和管理

### [mongo组件](./mongo/)

- 集成`mongo-driver`驱动
- 支持多个mongodb数据库配置和管理

## 链接

- [cherry](https://github.com/cherry-game/cherry)
- [cherry docs](https://cherry-game.github.io)

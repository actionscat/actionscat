# ActionsCat | 阿克神猫

面向 AI 生成代码的确定性、版本化、可调度、安全隔离的长期 Action 托管与执行平台。

[![License](https://img.shields.io/badge/license-MPL--2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25.3+-blue.svg)](https://golang.org)
[![CI](https://img.shields.io/badge/CI-passing-brightgreen.svg)](https://github.com/actionscat/actionscat/actions)

---

## 核心架构原则

ActionsCat 重构了传统的静态插件模式，构建了专为 AI 生成程序服务的可靠底座：

1. **业务解耦与代码 DSL**
   - ActionsCat Core 保持纯粹、确定性且与业务无关。核心服务仅管理 Actions、Versions、Builds、Artifacts、Runs、Schedules、Matchers、状态、日志及能力令牌。
   - 平台不内置任何第三方业务逻辑，用户编写的代码即为完整的业务 DSL。

2. **严格的 LLM 边界**
   - 大语言模型（LLM）仅作为 Action 创建与迭代时的“程序员”。
   - 在已部署 Action 的运行链路（Execution Hot Path）中严格排除 LLM，“忠诚于 if-else”，确保确定性与执行可控。

3. **清晰的领域分层**
   - 领域层级：`Action -> ActionVersion -> ArtifactBuild -> Run`。
   - Action 是状态主体；Run 是无状态、短生命周期的一次性沙箱执行，运行完成后即被彻底销毁。
   - 状态由 ActionsCat Core 统一部署管理（`/data/actions/action_<id>/state/`），工作节点严禁直接挂载宿主机状态目录。

4. **安全受控的状态回写**
   - 沙箱内通过 `pkg/actionscat` SDK 提供的 `WriteState` 回写状态，底层调用 Core 的 Runtime API（`POST /api/v1/runtime/state`）。
   - Core 严格校验路径安全性（杜绝路径遍历、绝对路径、Windows 盘符、NUL 字节及软链接穿越），严密校验单次运行能力凭证（Capability Token），强制忽略客户端伪造的 Action ID，并通过临时文件 + fsync + 原子重命名完成落盘。

5. **单次运行能力凭证（Run Capability Token）**
   - 每次 Run 生成 256 位加密安全随机 Token（`crypto/rand`），沙箱仅持有明文 Token。
   - Core 数据库仅持久化 `SHA-256(raw_token)` 哈希，且在 Run 结束（成功、失败或超时）时立即吊销。

6. **凭证隔离与受信代理**
   - 外部系统的长期凭证（如 FrostAgent API Key、数据库密码、管理密钥）严禁进入 Action 沙箱。
   - ActionsCat Core 充当受信代理（`POST /api/v1/runtime/frostagent/send`），沙箱仅需提供凭证 Token 及受限意图。

7. **构建与运行分离**
   - 源码构建运行于完全隔离的纯净构建沙箱中，零运行能力 Token、零状态访问权限、零管理凭证。
   - 构建产物（Artifact Bundle）可重现且可独立缓存。

8. **确定性匹配引擎（Deterministic Matcher）**
   - 彻底废除 Lua 脚本扫描，采用数据库持久化的结构化匹配引擎。
   - 支持 `exact`（完全匹配）、`contains`（包含匹配）以及 `regex`（正则表达式）。正则命名捕获组自动映射为环境变量传入 Action。

---

## 目录结构

```
actionscat/
├── cmd/
│   └── core/                    # ActionsCat Core 主程序入口
│       └── main.go
├── internal/
│   ├── action/                  # Action 元数据与版本服务
│   ├── adapter/                 # 外部事件协议适配器 (OneBot, HTTP, etc.)
│   ├── api/                     # Management API, Runtime API, Dispatch Handler
│   ├── build/                   # 构建编排与工具链对齐检查
│   ├── domain/                  # 核心领域模型 (Action, Version, Build, Run, Matcher, Schedule)
│   ├── frostagent/              # FrostAgent 代理客户端
│   ├── matcher/                 # 确定性消息匹配引擎
│   ├── runner/                  # 具备并发控制的 FIFO 执行器 Worker 线程池
│   ├── runtime/                 # 受信 Runtime 凭证校验与状态回写接口
│   ├── sandbox/                 # code-interpreter 沙箱中立 Backend 抽象与客户端
│   ├── scheduler/               # 纯生产、去重的 Cron 调度器
│   └── store/                   # SQLite (modernc.org/sqlite) 与文件存储引擎
├── pkg/
│   └── actionscat/              # 供 AI 生成 Action 使用的轻量 Go Runtime SDK
└── examples/
    └── actions/                 # 示例 Action 项目
        ├── bilibili_resolver/   # 视频解析 Action
        └── maimai_recorder/     # 玩家出勤记录 Action
```

---

## 快速开始

### 1. 编译核心服务

```powershell
go build -o actionscat.exe ./cmd/core
```

### 2. 启动服务

```powershell
# 可选环境变量配置
$env:ACTIONSCAT_ADDR = ":7999"
$env:ACTIONSCAT_DATA_DIR = "./data"
$env:FA_SANDBOX_ENDPOINT = "http://127.0.0.1:3874"
$env:FROSTAGENT_ENDPOINT = "http://127.0.0.1:8000"

./actionscat.exe
```

---

## API 端点概览

### 管理接口 (Management API)
- `POST /api/v1/actions`: 创建 Action
- `GET /api/v1/actions`: 获取 Action 列表
- `GET /api/v1/actions/:id`: 获取 Action 详情
- `PUT /api/v1/actions/:id`: 更新 Action 元数据及并发限制
- `DELETE /api/v1/actions/:id`: 删除 Action
- `POST /api/v1/actions/:id/versions`: 创建新版本（上传源码 Bundle）
- `GET /api/v1/actions/:id/versions`: 列出版本
- `POST /api/v1/actions/:id/versions/:vid/builds`: 发起构建
- `GET /api/v1/actions/:id/builds`: 获取构建历史
- `POST /api/v1/actions/:id/active-build`: 激活指定构建产物
- `GET /api/v1/actions/:id/toolchain-check`: 工具链版本基线对齐检查
- `POST /api/v1/actions/:id/runs`: 手动触发执行 Run
- `GET /api/v1/actions/:id/runs`: 获取执行历史及状态
- `POST /api/v1/actions/:id/schedules`: 创建 Cron 定时调度规则
- `POST /api/v1/actions/:id/matchers`: 创建消息匹配规则

### 事件分发接口 (Event Dispatch)
- `POST /api/v1/dispatch`: 消息事件路由分发入口（支持现代标准平台无关结构及传统 OneBot/QQ 格式）

### 运行时能力接口 (Runtime Capability API)
- `POST /api/v1/runtime/state`: 沙箱单次执行受控状态原子写入
- `POST /api/v1/runtime/frostagent/send`: 受信消息下发代理转发

---

## Action 编写示例 (使用 SDK)

AI 生成的独立 Action 项目仅需引入轻量 SDK `actionscat/pkg/actionscat`：

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"actionscat/pkg/actionscat"
)

type AttendanceState struct {
	Count       int       `json:"count"`
	LastUpdated time.Time `json:"last_updated"`
}

func main() {
	ctx := context.Background()
	_ = actionscat.GetContext()

	state := AttendanceState{
		Count:       4,
		LastUpdated: time.Now().UTC(),
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		log.Fatalf("failed to marshal state: %v", err)
	}

	// 原子回写状态至 ActionsCat Core
	if err := actionscat.WriteState(ctx, "attendance.json", stateBytes); err != nil {
		log.Fatalf("failed to write state: %v", err)
	}

	// 通过受控代理通道回复消息
	if err := actionscat.Reply(ctx, fmt.Sprintf("已成功打卡！当前在店人数: %d", state.Count)); err != nil {
		log.Fatalf("failed to reply: %v", err)
	}
}
```

---

## License

MPL-2.0 (see [LICENSE](LICENSE) file)

# ActionsCat | 阿克神猫

一个自动化工作流引擎，支持多种适配器，旨在简化自动化任务的创建和管理。

[![License](https://img.shields.io/badge/license-MPL--2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-blue.svg)](https://golang.org)
[![CI](https://img.shields.io/badge/CI-passing-brightgreen.svg)](https://github.com/GuaiZai/ActionsCat/actions)

## 构建

需要 Go 1.25+.

```powershell
# from project root
go build ./...
```

## 运行

```powershell
# set the listen address if needed
$env:ACTIONSCAT_ADDR = ":8080"
go run main.go
```

## Actions

由于 Golang 是一门静态编译语言，ActionsCat 目前不支持热加载插件，所有 Actions 都内置于主程序中。

#### 支持的 Actions 示例：
- **舞萌机厅人数**：通过玩家自发更新，实时查看排卡情况。
![https://github.com/actionscat/assets/blob/main/maimai-player-count.png?raw=true](https://github.com/actionscat/assets/blob/main/maimai-player-count.png?raw=true)

- **视频解析**：根据视频链接获取元数据。
![https://github.com/actionscat/assets/blob/main/bili-resolver.png?raw=true](https://github.com/actionscat/assets/blob/main/bili-resolver.png?raw=true)

## Contributing
See `CONTRIBUTING.md`.

## License
MPL-2.0 (see LICENSE file)

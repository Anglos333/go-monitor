# 🚀 Go-Monitor: 基于 Go 协程的高并发服务监控系统 (V15.0)

![Go](https://img.shields.io/badge/Go-1.20+-00ADD8?style=flat&logo=go)
![SQLite](https://img.shields.io/badge/SQLite-Integrated-003B57?style=flat&logo=sqlite)
![License](https://img.shields.io/badge/License-MIT-green.svg)

> 一个轻量级、高性能、跨平台的分布式服务监控与告警平台。
> 具备 **并发探测、自我感知、数据持久化、智能告警抑制** 等核心特性。

## ✨ 核心功能 (Core Features)

1.  **⚡ 高并发探测**：基于 Goroutine + Channel 模型，支持秒级并发监控数百个服务节点。
2.  **📱 响应式后台**：基于 CSS Media Query 实现的自适应管理面板，完美适配 PC 与 移动端。
3.  **🛡️ 智能告警抑制**：
    * **防抖 (Debounce)**：连续失败 N 次才报警，拒绝网络抖动误报。
    * **静默 (Silence)**：报警后进入冷却期，防止邮件轰炸。
4.  **📧 邮件闭环通知**：支持 SMTP (SSL/TLS) 协议，故障发生与恢复均有实时邮件推送。
5.  **💾 数据持久化**：
    * 内置 **SQLite** 嵌入式数据库，无需安装 MySQL 即可运行。
    * 使用 **GORM** (Code-First) 自动迁移表结构。
6.  **📊 可视化图表**：集成 **ECharts**，实时绘制服务响应时间趋势曲线。
7.  **🧠 系统自我感知**：实时监控自身 Goroutine 数量、内存占用 (Mem) 及运行时间 (Uptime)。
8.  **📥 数据导出**：支持一键导出审计日志为 CSV 格式，便于二次分析。

## 🛠️ 技术栈 (Tech Stack)

* **后端**：Go (Golang)
* **数据库**：SQLite3 + GORM
* **前端**：HTML5 + CSS3 (Responsive) + ECharts
* **邮件服务**：Go-Gomail (支持 SSL)

## 🚀 快速开始 (Quick Start)

### 1. 环境要求
* Go 1.18 或更高版本
* 网络连接 (用于下载依赖和发送邮件)

### 2. 运行项目
```bash
# 1. 下载依赖
go mod tidy

# 2. 运行程序
go run main.go

# 3. 访问管理后台
# 打开浏览器访问: [http://127.0.0.1:9090](http://127.0.0.1:9090)
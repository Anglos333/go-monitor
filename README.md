# 🚀 Go-Monitor: 企业级轻量高并发服务监控系统 (V15.1 最终版)

![Go Version](https://img.shields.io/badge/Go-1.20+-00ADD8?style=flat&logo=go)
![SQLite](https://img.shields.io/badge/SQLite-Integrated-003B57?style=flat&logo=sqlite)
![Architecture](https://img.shields.io/badge/Architecture-Standard_Go_Layout-brightgreen)
![Security](https://img.shields.io/badge/Security-AES--256--GCM-red)
![License](https://img.shields.io/badge/License-MIT-blue.svg)

> 一个极简、高性能、零外部依赖的分布式服务可用性监控与告警平台。
> 具备 **并发探测、状态机告警抑制、凭证动态加密、单文件秒级部署** 等企业级核心特性。

## ✨ 核心创新点与特性 (Core Features)

1. **⚡ 基于 CSP 模型的高并发调度**
   - 摒弃传统多线程，深度应用 Go 协程 (`Goroutine`) 与通道 (`Channel`)。
   - 实现极低资源开销下的海量探针实时调度与无阻塞 I/O 探测。

2. **🛡️ 智能告警抑制算法 (状态机模型)**
   - **滑动窗口防抖 (Debounce)**：支持配置 `AlertThreshold`，连续失败 N 次才确认为真实宕机，完美过滤网络瞬时抖动。
   - **静默冷却机制 (Cooldown Silence)**：触发告警后进入休眠期，彻底告别“告警风暴 (Alert Fatigue)”。
   - **全生命周期闭环**：打通“异常发现 -> 邮件告警 -> 恢复感知 -> 自动消警”完整流程。

3. **🔒 敏感数据硬编码防护 (AES-256-GCM)**
   - 摒弃明文存储，系统在持久化配置层引入 AES 对称加密算法。
   - 内存加载时即时解密，落盘保存时动态加密，杜绝 `config.json` 泄露导致的核心授权码/密码被盗风险。

4. **📦 开箱即用的单文件部署 (go:embed)**
   - 前端 Web 静态资源 (HTML/CSS/JS) 全面采用 Go 1.16+ 的 `//go:embed` 魔法。
   - 编译后仅生成**单一可执行文件**，无需携带 `templates` 文件夹，彻底消除环境路径依赖。

5. **📱 极客范的响应式全栈 Web 控制台**
   - 自动适配 PC 与移动端 (CSS Media Queries)。
   - 支持 **Light/Dark 自动深色模式**、自适应 ECharts 数据趋势图。
   - 支持**任务标星置顶 (Star)** 与 ID 智能排序，强迫症福音。
   - 后台自动监测自身系统状态（Uptime、Goroutines、Memory）。

## 📂 工程目录结构 (Standard Go Layout)

本项目严格遵循 Go 语言标准项目目录规范进行分层重构：

```text
go-monitor/
├── cmd/
│   └── server/
│       └── main.go           # 程序的唯一启动入口 (指挥官)
├── internal/                 # 内部核心业务逻辑 (不可被外部包导入)
│   ├── config/               # 配置管理中心 (包含 AES-256 加解密逻辑与发号器)
│   ├── model/                # 全局数据模型 (Structs / DB Models)
│   ├── monitor/              # 监控核心引擎 (HTTP Client 探测、状态机与邮件发送)
│   ├── repository/           # 数据仓储层 (封装 GORM 对 SQLite 的操作)
│   └── web/                  # HTTP 展现层
│       ├── handler.go        # 路由分发与 API 接口
│       └── templates/        # 存放 index.html (被 go:embed 打包嵌入)
├── config.example.json       # 配置模板 (脱敏)
├── go.mod / go.sum           # 依赖管理
└── README.md                 # 项目文档

```

## 🛠️ 技术栈 (Tech Stack)

* **底层语言**：Go (Golang)
* **持久化方案**：SQLite3 + GORM (Code-First 自动建表)
* **前端实现**：原生 HTML5 + CSS3 (Variables 驱动主题) + ECharts.js
* **邮件服务**：Go-Gomail (支持 SSL/TLS 协议)
* **安全加密**：`crypto/aes` (AES-GCM-256)

## 🚀 快速开始 (Quick Start)

### 1. 环境准备

确保您的计算机上已安装 Go (>=1.20) 环境。

### 2. 获取代码与依赖

```bash
git clone [https://github.com/您的用户名/go-monitor.git](https://github.com/您的用户名/go-monitor.git)
cd go-monitor
go mod tidy

```

### 3. 编译为单文件 (推荐)

```bash
go build -o HakimiMonitor.exe ./cmd/server

```

*编译完成后，您只需将 `HakimiMonitor.exe` 拷贝到任意无环境的机器或服务器上，双击即可运行！*

### 4. 访问管理面板

程序启动后，打开浏览器访问：[http://127.0.0.1:9090](http://127.0.0.1:9090)

## ⚙️ 配置文件说明

系统首次启动会自动生成 `config.json` 和 `monitor.db` 数据库。
你可以通过 Web 后台的 **“⚙️ 系统设置”** 直接进行可视化配置。若手动修改 JSON，核心参数如下：

```json
{
  "interval": 5,             // 监控探测频率 (秒)
  "alert_threshold": 3,      // 防抖：连续失败几次视为宕机
  "alert_cooldown": 60,      // 静默：报警邮件发送后的冷却时间 (分钟)
  "next_task_id": 10,        // 自增发号器 (严禁手动调小，防止历史数据串位)
  "smtp": {
    "enabled": true,         // 是否开启告警
    "host": "smtp.qq.com",
    "port": 465,             // SSL 端口
    "username": "your_email@qq.com",
    "password": "加密后的密文(后台填入明文保存后会自动加密)", 
    "to": "receive_email@qq.com"
  }
}

```

## 📸 运行截图

*(建议在此处放置 2-3 张截图，如：深色模式的控制台主页、ECharts 趋势弹窗、收到的报警邮件截图)*

---

**Author**: [你的名字]

**Date**: 2026-02

**Version**: V15.1 (Graduation Project Final Release)

 


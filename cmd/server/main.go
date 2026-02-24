package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"monitor/internal/config"
	"monitor/internal/monitor"
	"monitor/internal/repository"
	"monitor/internal/web"
)

// 执行顺序：
// 1. 记录启动时间，用于后续页面显示运行时长。
// 2. 初始化配置管理器，加载配置文件，若失败则使用默认配置。
// 3. 初始化数据库仓储层，用于持久化存储监控结果。
// 4. (已迁移) 模板解析现在交由 web 包通过 go:embed 内部处理！
// 5. 创建监控核心实例，并启动监控循环（独立goroutine）。
// 6. 如果配置了SMTP，则异步执行邮件自检，确保系统重启时能发送通知。
// 7. 创建Web处理器，注册路由，并启动HTTP服务器监听9090端口。
func main() {
	start := time.Now()
	fmt.Println("🚀 哈基米监控系统（单文件部署终极版）启动...")

	cfgMgr := config.NewManager("config.json")
	if err := cfgMgr.LoadOrDefault(); err != nil {
		log.Fatal("load config failed:", err)
	}

	repo, err := repository.New("monitor.db")
	if err != nil {
		log.Fatal("init db failed:", err)
	}

	// ❌ 这里原本有 template.ParseFiles，现在光荣下岗了！

	mon := monitor.New(cfgMgr, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mon.Start(ctx)

	// 如果SMTP功能已启用，则进行邮件自检
	if cfgMgr.Get().SMTP.Enabled {
		go func() {
			fmt.Println("📧 正在后台进行邮件自检...")
			if err := mon.SendStartupCheckMail(); err != nil {
				fmt.Println("❌ 邮件自检失败:", err)
			} else {
				fmt.Println("✅ 邮件自检通过")
			}
		}()
	}

	// ✅ 创建Web处理器：注意这里的参数，已经把 tpl 去掉了！
	h := web.New(cfgMgr, repo, mon, start)
	mux := http.NewServeMux()
	h.Register(mux)

	addr := ":9090"
	fmt.Println("🌐 管理后台:", "http://127.0.0.1"+addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

## 改进清单（仅规划，不改代码）

### 安全 / 配置
1. SMTP 密码硬编码密钥调整为环境变量注入，避免源码泄露。
   - 位置：[internal/config/manager.go:20-83](internal/config/manager.go:20)
   - 做法：secretKey 从 os.Getenv 读取，缺省报错或使用随机；同时在 README 提示。

2. SMTP 账号缺少多收件人/抄送/密送支持。
   - 位置：[internal/model/types.go:19-27](internal/model/types.go:19)；[internal/monitor/service.go:305-321](internal/monitor/service.go:305)
   - 做法：支持逗号/分号拆分 To，增加 Cc/Bcc 字段。

3. HTTP 请求未设置 User-Agent，部分站点可能拒绝；未限制重定向可能导致探测跑偏。
   - 位置：[internal/monitor/service.go:267-301](internal/monitor/service.go:267)
   - 做法：自定义 Transport 的 CheckRedirect，设置 UA。

4. URL 探测允许 http:// 降级，但未强制 HSTS；可提供“仅 https”开关。
   - 位置：[internal/config/manager.go:136-181](internal/config/manager.go:136)
   - 做法：配置项 force_https，添加校验。

5. 配置文件加密写回时使用明文密钥存储在代码中，建议在 README 添加部署指引，支持环境变量覆盖配置路径/密钥。
   - 位置：[internal/config/manager.go:20-83](internal/config/manager.go:20)

### 健壮性 / 可靠性
6. 监控间隔 <1 时会被重置为 5s，建议在前端校验并提示；防止 0/负数穿透。
   - 位置：[internal/config/manager.go:84-128](internal/config/manager.go:84)；前端 [internal/web/templates/index.html](internal/web/templates/index.html)

7. 删除任务后 history/states 清理已做，但性能日志/事件日志仍保留；可提供“连同历史删除”选项。
   - 位置：[internal/monitor/service.go:106-121](internal/monitor/service.go:106)；[internal/repository/db.go:64-68](internal/repository/db.go:64)

8. fetch 失败时前端静默（图表加载失败），用户感知弱。
   - 位置：[internal/web/templates/index.html:823-866](internal/web/templates/index.html:823)
   - 做法：toast/alert 提示，或界面状态占位。

9. 并发检查 goroutine 泄漏风险较低但 runBatch 忽略 channel close；可使用 WaitGroup 或 errgroup 简化并发与收集。
   - 位置：[internal/monitor/service.go:123-245](internal/monitor/service.go:123)

10. HTTP Client 超时固定 5s，可作为配置项暴露（探测超时、TLS 握手超时）。
    - 位置：[internal/monitor/service.go:37-53](internal/monitor/service.go:37)

11. probeURL HEAD/GET 未区分 429/403 等限流/封禁状态，可能导致误判。
    - 位置：[internal/web/handler.go:239-269](internal/web/handler.go:239)
    - 做法：把 >=400 && <500 视为可疑并提示。

12. SQLite 连接未设置忙等待/锁退避，写入争用下可能失败。
    - 位置：[internal/repository/db.go:18-27](internal/repository/db.go:18)
    - 做法：sqlite.Open DSN 带 `_busy_timeout`。

### 性能 / 体验
13. 前端每 5s 强制 reload 以获取新数据，体验和带宽浪费，可改为轮询 JSON + 局部更新。
    - 位置：[internal/web/templates/index.html:884-887](internal/web/templates/index.html:884)

14. 趋势图一次只取 50 条，可支持分页/最近 10 分钟/1 小时下拉选择。
    - 位置：[internal/web/handler.go:188-207](internal/web/handler.go:188)

15. 任务列表缺少筛选（仅失败/仅标星）与排序（耗时、状态）。
    - 位置：前端 [internal/web/templates/index.html](internal/web/templates/index.html)

16. 新任务添加后仅 alert 提示，可改为非阻塞 toast，减少 reload。
    - 位置：[internal/web/templates/index.html:712-748](internal/web/templates/index.html:712)

17. 图表主题色与全局主题联动已做，但 tooltip 背景写死为白底，可同步使用 CSS 变量。
    - 位置：[internal/web/templates/index.html:827-866](internal/web/templates/index.html:827)

### 数据模型 / 统计
18. PerformanceLog 无索引，趋势查询按 id desc 依赖主键；可为 task_id 建索引。
    - 位置：[internal/model/types.go:69-76](internal/model/types.go:69) + AutoMigrate。

19. EventLog 缺少任务级筛选/分页 API；前端日志列表拉全表（默认 50）。
    - 位置：[internal/web/handler.go:78-88](internal/web/handler.go:78)

20. 缺少全局健康检查端点（/healthz），便于部署探针。
    - 位置：[internal/web/handler.go:46-57](internal/web/handler.go:46)

### 加分小巧思
21. 任务“标星”已支持，可增加“分组/标签”字段做分组展示与过滤。
    - 位置：模型 [internal/model/types.go:29-50](internal/model/types.go:29)；前端表格。

22. 告警抑制策略增加“工作时间段”与“静默窗口”（cron-like）。
    - 位置：配置 [internal/model/types.go:9-18](internal/model/types.go:9)；监控逻辑 [internal/monitor/service.go:123-245](internal/monitor/service.go:123)

23. 邮件内容增加最近 N 次响应时间 mini sparkline（ASCII 或内联图片）。
    - 位置：[internal/monitor/service.go:305-321](internal/monitor/service.go:305)

24. 支持 WebHook（钉钉/企微/飞书）作为告警渠道。
    - 位置：新增配置与发送器；复用告警分发逻辑。

25. 导出 CSV 增加 UTF-8 BOM 已有，可附带耗时统计（平均/99分位）。
    - 位置：[internal/web/handler.go:223-237](internal/web/handler.go:223)

26. 提供“演示模式”开关，隐藏真实 URL/邮箱，便于答辩展示。
    - 位置：前端渲染时做脱敏；配置新增 demo_mode。


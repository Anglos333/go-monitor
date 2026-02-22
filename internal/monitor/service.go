// Package monitor 实现网站监控核心逻辑：定时执行 HTTP 检查，管理任务状态，
// 根据失败次数触发告警和恢复通知，并记录历史数据。
package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"monitor/internal/config"
	"monitor/internal/model"
	"monitor/internal/repository"

	"gopkg.in/gomail.v2"
)

// Service 是监控服务的主结构，负责定时检查任务、维护状态、发送告警。
type Service struct {
	cfg  *config.Manager  // 配置管理器，用于获取最新配置
	repo *repository.Repo // 数据仓储，用于持久化日志

	client *http.Client // 自定义 HTTP 客户端，设置超时和连接池

	mu      sync.RWMutex             // 保护 results、states、history 的并发访问
	runMu   sync.Mutex               // 防止手动触发和定时循环并发执行 runBatch
	results []model.MonitorResult    // 当前所有任务的最新检查结果（用于 Web 展示）
	states  map[int]*model.TaskState // 每个任务的动态状态（失败计数、是否宕机、上次告警时间）
	history map[string][]string      // 每个 URL 的历史状态颜色点（最近10次）
}

// New 创建监控服务实例，初始化 HTTP 客户端和内部状态容器。
func New(cfg *config.Manager, repo *repository.Repo) *Service {
	return &Service{
		cfg:  cfg,
		repo: repo,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		states:  map[int]*model.TaskState{},
		history: map[string][]string{},
	}
}

// Start 启动监控循环，按配置的间隔定时执行检查。收到 ctx.Done() 时退出。
func (s *Service) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c := s.cfg.Get()
		s.runOnce(c.Tasks, c.AlertThreshold, c.AlertCooldown)

		interval := c.Interval
		if interval <= 0 {
			interval = 5
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
}

// TriggerNow 触发立即执行一次检查（用于手动刷新）。
func (s *Service) TriggerNow() {
	c := s.cfg.Get()
	go s.runOnce(c.Tasks, c.AlertThreshold, c.AlertCooldown)
}

// runOnce 在 runMu 的保护下调用 runBatch，确保同一时间只有一个检查批次在执行。
func (s *Service) runOnce(tasks []model.MonitorTask, threshold, cooldownMin int) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.runBatch(tasks, threshold, cooldownMin)
}

// SendStartupCheckMail 发送启动自检邮件，验证 SMTP 配置是否正确。
func (s *Service) SendStartupCheckMail() error {
	return s.sendMail("✅ [自检] 系统启动", "邮件服务配置正常！")
}

// Results 返回当前所有任务的最新检查结果副本，供 Web 页面使用。
func (s *Service) Results() []model.MonitorResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.MonitorResult, len(s.results))
	copy(out, s.results)
	return out
}

// RemoveTaskState 删除指定任务的所有状态（states、history、results），用于任务删除后清理。
func (s *Service) RemoveTaskState(taskID int, taskURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, taskID)
	delete(s.history, taskURL)

	// 从结果切片中移除该任务
	filtered := make([]model.MonitorResult, 0, len(s.results))
	for _, r := range s.results {
		if r.ID != taskID {
			filtered = append(filtered, r)
		}
	}
	s.results = filtered
}

// runBatch 并发检查所有任务，更新状态并处理告警/恢复逻辑。
// 参数：
//
//	tasks: 当前任务列表
//	threshold: 连续失败触发告警的次数
//	cooldownMin: 告警冷却时间（分钟），防止频繁发送同任务告警
func (s *Service) runBatch(tasks []model.MonitorTask, threshold, cooldownMin int) {
	if len(tasks) == 0 {
		return
	}
	if threshold <= 0 {
		threshold = 1
	}
	cooldown := time.Duration(cooldownMin) * time.Minute
	if cooldown < 0 {
		cooldown = 0
	}

	// 并发执行检查，结果通过 channel 收集
	ch := make(chan model.MonitorResult, len(tasks))
	for _, t := range tasks {
		go s.checkURL(t, ch)
	}

	newResults := make([]model.MonitorResult, 0, len(tasks))

	for i := 0; i < len(tasks); i++ {
		res := <-ch

		// 如果检查成功，记录性能日志
		if res.IsSuccess {
			s.repo.CreatePerformance(&model.PerformanceLog{
				TaskID:       res.ID,
				TaskName:     res.TaskName,
				ResponseTime: res.DurationInt,
				CheckTime:    time.Now().Format("15:04:05"),
			})
		}

		// 更新历史点阵（保留最近10次）
		s.mu.Lock()
		his := append(s.history[res.URL], res.StatusColor)
		if len(his) > 10 {
			his = his[len(his)-10:]
		}
		s.history[res.URL] = his
		res.HistoryDots = append([]string(nil), his...)

		// 获取或创建任务状态
		st, ok := s.states[res.ID]
		if !ok {
			st = &model.TaskState{}
			s.states[res.ID] = st
		}

		shouldAlert := false
		needRecover := false
		failCount := 0

		// 告警/恢复判定逻辑
		if !res.IsSuccess {
			// 失败：递增连续失败次数
			st.ConsecutiveFails++
			failCount = st.ConsecutiveFails
			if st.ConsecutiveFails == threshold {
				// 首次达到阈值，标记为宕机并触发告警
				st.IsDown = true
				shouldAlert = true
			} else if st.ConsecutiveFails > threshold && time.Since(st.LastAlertTime) > cooldown {
				// 持续失败且冷却期已过，再次触发告警
				shouldAlert = true
			}
			if shouldAlert {
				st.LastAlertTime = time.Now()
			}
		} else {
			// 成功：如果之前是宕机状态，则触发恢复
			if st.IsDown {
				needRecover = true
			}
			st.IsDown = false
			st.ConsecutiveFails = 0
		}
		s.mu.Unlock()

		// 处理告警
		if shouldAlert {
			msg := fmt.Sprintf("服务 [%s] 确认故障! (连续失败%d次, 响应码:%d)", res.TaskName, failCount, res.StatusCode)
			s.repo.CreateEvent(&model.EventLog{
				TaskName:  res.TaskName,
				EventTime: time.Now().Format("2006-01-02 15:04:05"),
				Type:      "🔥 宕机警告",
				Message:   msg,
			})
			// 异步发送邮件，避免阻塞主流程
			go func() {
				_ = s.sendMail(fmt.Sprintf("🔥 [报警] %s 宕机 (累积失败%d次)", res.TaskName, failCount), msg)
			}()
		}

		// 处理恢复
		if needRecover {
			msg := fmt.Sprintf("服务 [%s] 已恢复正常。耗时: %s", res.TaskName, res.Duration)
			s.repo.CreateEvent(&model.EventLog{
				TaskName:  res.TaskName,
				EventTime: time.Now().Format("2006-01-02 15:04:05"),
				Type:      "✅ 故障恢复",
				Message:   msg,
			})
			s.repo.ResolveDownEvents(res.TaskName) // 将历史未恢复的告警标记为已恢复
			go func() {
				_ = s.sendMail("✅ [恢复] 服务恢复: "+res.TaskName, msg)
			}()
		}

		newResults = append(newResults, res)
	}

	// 更新全局结果切片
	s.mu.Lock()
	s.results = newResults
	s.mu.Unlock()
}

// checkURL 对单个任务执行 HTTP GET 请求，生成 MonitorResult。
// 结果通过 channel 返回，实现并发收集。
func (s *Service) checkURL(task model.MonitorTask, ch chan<- model.MonitorResult) {
	start := time.Now()
	res := model.MonitorResult{
		ID:         task.ID,
		TaskName:   task.Name,
		URL:        task.URL,
		LastUpdate: time.Now().Format("15:04:05"),
	}

	// 预先验证 URL 格式，避免无效请求
	if _, err := url.ParseRequestURI(task.URL); err != nil {
		res.Status, res.StatusColor = "故障", "red"
		res.Duration = "0ms"
		ch <- res
		return
	}

	req, err := http.NewRequest(http.MethodGet, task.URL, nil)
	if err != nil {
		res.Status, res.StatusColor = "故障", "red"
		res.Duration = "0ms"
		ch <- res
		return
	}

	resp, err := s.client.Do(req)
	ms := time.Since(start).Milliseconds()
	res.Duration = fmt.Sprintf("%dms", ms)
	res.DurationInt = ms

	if err != nil {
		// 网络错误、超时等视为故障
		res.Status, res.StatusColor = "故障", "red"
		ch <- res
		return
	}
	defer resp.Body.Close()
	// 读取并丢弃响应体以复用连接
	_, _ = io.Copy(io.Discard, resp.Body)

	res.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		res.IsSuccess = true
		if ms > 800 {
			// 响应时间超过800ms标记为“缓慢”
			res.Status, res.StatusColor = "缓慢", "yellow"
		} else {
			res.Status, res.StatusColor = "正常", "green"
		}
	} else {
		res.Status, res.StatusColor = "故障", "red"
	}
	ch <- res
}

// sendMail 通过 SMTP 发送邮件，使用配置中的账号信息。
// 如果 SMTP 未启用，则直接返回 nil 不发送。
func (s *Service) sendMail(subject, body string) error {
	cfg := s.cfg.Get().SMTP
	if !cfg.Enabled {
		return nil
	}
	m := gomail.NewMessage()
	m.SetHeader("From", cfg.Username)
	m.SetHeader("To", cfg.To)
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", body+"\r\n\r\n----------------\r\n来自：哈基米监控系统")

	d := gomail.NewDialer(cfg.Host, cfg.Port, cfg.Username, cfg.Password)
	d.TLSConfig = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	return d.DialAndSend(m)
}

// Package web 提供 HTTP 服务，包括管理后台页面渲染、API 接口（添加/删除任务、更新配置、图表数据、系统状态等）。
package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"monitor/internal/config"
	"monitor/internal/model"
	"monitor/internal/monitor"
	"monitor/internal/repository"
)

// Handler 聚合了配置、仓储、监控服务以及模板，处理所有 HTTP 请求。
type Handler struct {
	cfg   *config.Manager
	repo  *repository.Repo
	mon   *monitor.Service
	start time.Time
	tpl   *template.Template
}

// New 创建 Web 处理器实例。
func New(cfg *config.Manager, repo *repository.Repo, mon *monitor.Service, tpl *template.Template, start time.Time) *Handler {
	return &Handler{cfg: cfg, repo: repo, mon: mon, tpl: tpl, start: start}
}

// Register 将路由及其对应的处理函数注册到 ServeMux。
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", h.webHandler)
	mux.HandleFunc("/api/chart", h.chartDataHandler)
	mux.HandleFunc("/api/task/add", h.addTaskHandler)
	mux.HandleFunc("/api/task/delete", h.deleteTaskHandler)
	mux.HandleFunc("/api/settings/update", h.updateSettingsHandler)
	mux.HandleFunc("/api/logs/clear", h.clearLogsHandler)
	mux.HandleFunc("/api/sys/stats", h.sysStatsHandler)
	mux.HandleFunc("/api/logs/export", h.exportCsvHandler)
}

// webHandler 渲染主页面，传入当前监控结果、最近事件日志和配置（隐藏密码）。
func (h *Handler) webHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		return
	}
	cfg := h.cfg.Get()
	cfg.SMTP.Password = "" // 不将密码返回到前端
	data := struct {
		Results []model.MonitorResult
		Logs    []model.EventLog
		Config  model.Config
	}{
		Results: h.mon.Results(),
		Logs:    h.repo.QueryEvents(50),
		Config:  cfg,
	}
	_ = h.tpl.Execute(w, data)
}

// addTaskHandler 处理添加监控任务的请求。
// 支持 force 参数跳过连通性校验，添加成功后立即触发一次监控检查。
func (h *Handler) addTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Force bool   `json:"force"` // 是否强制添加（跳过连通性校验）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)

	// 按相同规则补全协议（用于探测）
	testURL := req.URL
	if !strings.HasPrefix(testURL, "http://") && !strings.HasPrefix(testURL, "https://") {
		testURL = "https://" + testURL
	}

	// 若非强制模式，进行连通性校验
	if !req.Force {
		if err := probeURL(testURL); err != nil {
			http.Error(w, "连通性校验失败: "+err.Error()+"（可选择强制添加）", http.StatusUnprocessableEntity)
			return
		}
	}

	_, err := h.cfg.AddTask(req.Name, req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.mon.TriggerNow() // 立即执行一轮检查，让新任务快速显示结果
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// deleteTaskHandler 处理删除任务的请求，并从监控状态中清理相关数据。
func (h *Handler) deleteTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	delURL, err := h.cfg.DeleteTask(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mon.RemoveTaskState(req.ID, delURL) // 清理监控服务中的缓存状态
	w.WriteHeader(http.StatusOK)
}

// updateSettingsHandler 更新全局配置，保存后立即触发一轮检查应用新设置。
func (h *Handler) updateSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in model.Config
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.cfg.UpdateSettings(in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 配置更新后立即按新配置跑一轮
	h.mon.TriggerNow()

	w.WriteHeader(http.StatusOK)
}

// clearLogsHandler 清空所有事件日志和性能日志。
func (h *Handler) clearLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.repo.ClearLogs()
	w.WriteHeader(http.StatusOK)
}

// chartDataHandler 返回指定任务的最近 50 条性能数据（时间点和响应时间），用于前端图表展示。
func (h *Handler) chartDataHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	logs := h.repo.QueryPerformance(id, 50)
	out := struct {
		Times  []string `json:"times"`
		Values []int64  `json:"values"`
	}{}
	// 按时间正序返回，方便图表绘制
	for i := len(logs) - 1; i >= 0; i-- {
		out.Times = append(out.Times, logs[i].CheckTime)
		out.Values = append(out.Values, logs[i].ResponseTime)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// sysStatsHandler 返回系统运行状态（协程数、内存使用、运行时长）。
func (h *Handler) sysStatsHandler(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	up := time.Since(h.start)
	stats := map[string]any{
		"goroutines": runtime.NumGoroutine(),
		"memory":     fmt.Sprintf("%.2f MB", float64(m.Alloc)/1024/1024),
		"uptime":     fmt.Sprintf("%02d:%02d:%02d", int(up.Hours()), int(up.Minutes())%60, int(up.Seconds())%60),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

// exportCsvHandler 导出所有事件日志为 CSV 文件，包含 UTF-8 BOM 头以便 Excel 正确打开。
func (h *Handler) exportCsvHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Disposition", "attachment; filename=monitor_logs.csv")
	w.Header().Set("Content-Type", "text/csv")
	// 写入 UTF-8 BOM，使 Excel 识别中文
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"ID", "时间", "任务名称", "类型", "消息内容", "是否修复"})
	for _, l := range h.repo.QueryEvents(0) {
		_ = writer.Write([]string{
			fmt.Sprintf("%d", l.ID), l.EventTime, l.TaskName, l.Type, l.Message, fmt.Sprintf("%v", l.IsResolved),
		})
	}
	writer.Flush()
}

// probeURL 尝试通过 HEAD 请求探测 URL 连通性，若 HEAD 不支持则回退到 GET 请求。
// 只检查状态码是否 <500（非服务端错误），超时或网络错误视为失败。
func probeURL(raw string) error {
	client := &http.Client{Timeout: 4 * time.Second}

	// 先 HEAD
	req, _ := http.NewRequest(http.MethodHead, raw, nil)
	resp, err := client.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		// 405 表示不支持 HEAD，不算失败
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusMethodNotAllowed {
			return nil
		}
	}

	// 再 GET 兜底
	req2, _ := http.NewRequest(http.MethodGet, raw, nil)
	resp2, err2 := client.Do(req2)
	if err2 != nil {
		return err2
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode >= 500 {
		return fmt.Errorf("状态码异常: %d", resp2.StatusCode)
	}
	return nil
}

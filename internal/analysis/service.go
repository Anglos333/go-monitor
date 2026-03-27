package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"monitor/internal/config"
	"monitor/internal/model"
	"monitor/internal/monitor"
	"monitor/internal/repository"
)

// Service 负责聚合监控结果、事件日志与性能日志，输出稳定性智能分析结果。
type Service struct {
	cfg  *config.Manager
	repo *repository.Repo
	mon  *monitor.Service

	mu        sync.Mutex
	cached    model.StabilityAnalysis
	expiresAt time.Time
}

type llmChatRequest struct {
	Model       string           `json:"model"`
	Temperature float64          `json:"temperature"`
	Messages    []llmChatMessage `json:"messages"`
}

type llmChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type llmNarrative struct {
	Summary     string   `json:"summary"`
	RiskReason  string   `json:"risk_reason"`
	Suggestions []string `json:"suggestions"`
}

// New 创建稳定性分析服务实例。
func New(cfg *config.Manager, repo *repository.Repo, mon *monitor.Service) *Service {
	return &Service{cfg: cfg, repo: repo, mon: mon}
}

// Reset 在数据库连接被重建后刷新仓储引用并清空缓存。
func (s *Service) Reset(repo *repository.Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repo = repo
	s.cached = model.StabilityAnalysis{}
	s.expiresAt = time.Time{}
}

// Get 返回当前稳定性分析结果；默认使用缓存，force=true 时强制重新生成。
func (s *Service) Get(force bool) model.StabilityAnalysis {
	s.mu.Lock()
	defer s.mu.Unlock()

	analysisCfg := s.cfg.Get().Analysis
	if !analysisCfg.Enabled {
		return buildDisabledAnalysis()
	}

	if !force && !s.expiresAt.IsZero() && time.Now().Before(s.expiresAt) && s.cached.GeneratedAt != "" {
		return cloneAnalysis(s.cached)
	}

	result := s.build(analysisCfg)
	cacheSeconds := analysisCfg.CacheSeconds
	if cacheSeconds <= 0 {
		cacheSeconds = 60
	}
	s.cached = result
	s.expiresAt = time.Now().Add(time.Duration(cacheSeconds) * time.Second)
	return cloneAnalysis(result)
}

func (s *Service) build(cfg model.AnalysisConfig) model.StabilityAnalysis {
	results := s.mon.Results()
	states := s.mon.StateSnapshot()
	openAlerts := s.repo.QueryOpenAlerts()
	recentEvents := s.repo.QueryEvents(cfg.DetailEventLimit)

	openAlertMap := make(map[string]int)
	for _, evt := range openAlerts {
		openAlertMap[evt.TaskName]++
	}

	snapshot := model.StabilitySnapshot{
		TotalTasks:       len(results),
		UnresolvedAlerts: len(openAlerts),
	}
	for _, evt := range recentEvents {
		if evt.Type == "✅ 故障恢复" {
			snapshot.RecentRecoveries++
		}
	}

	taskBreakdown := make([]model.StabilityTaskDetail, 0, len(results))
	for _, res := range results {
		state := states[res.ID]
		avgResponse, lastResponse := summarizePerformance(s.repo.QueryPerformance(res.ID, cfg.PerformanceSampleSize), res.DurationInt)

		taskDetail := model.StabilityTaskDetail{
			TaskID:           res.ID,
			TaskName:         res.TaskName,
			Status:           res.Status,
			StatusColor:      res.StatusColor,
			FailureStreak:    state.ConsecutiveFails,
			AvgResponseMS:    avgResponse,
			LastResponseMS:   lastResponse,
			UnresolvedAlerts: openAlertMap[res.TaskName],
		}
		taskDetail.RiskScore, taskDetail.Evidence = scoreTask(res, state, taskDetail.UnresolvedAlerts, avgResponse, cfg.SlowThresholdMS)
		taskDetail.RiskLevel = riskLevel(taskDetail.RiskScore)

		switch res.StatusColor {
		case "red":
			snapshot.FailedTasks++
		case "yellow":
			snapshot.SlowTasks++
		case "green":
			snapshot.HealthyTasks++
		default:
			snapshot.HealthyTasks++
		}

		taskBreakdown = append(taskBreakdown, taskDetail)
	}

	sort.Slice(taskBreakdown, func(i, j int) bool {
		if taskBreakdown[i].RiskScore != taskBreakdown[j].RiskScore {
			return taskBreakdown[i].RiskScore > taskBreakdown[j].RiskScore
		}
		if taskBreakdown[i].FailureStreak != taskBreakdown[j].FailureStreak {
			return taskBreakdown[i].FailureStreak > taskBreakdown[j].FailureStreak
		}
		return taskBreakdown[i].TaskID < taskBreakdown[j].TaskID
	})

	overallScore := scoreOverall(snapshot, taskBreakdown)
	analysis := model.StabilityAnalysis{
		Enabled:         true,
		GeneratedAt:     time.Now().Format("2006-01-02 15:04:05"),
		Source:          "local-rule",
		AbnormalSummary: buildLocalSummary(snapshot, taskBreakdown),
		RiskAssessment: model.RiskAssessment{
			Score:  overallScore,
			Level:  riskLevel(overallScore),
			Reason: buildRiskReason(snapshot, taskBreakdown),
		},
		HandlingSuggestions: buildSuggestions(snapshot, taskBreakdown),
		Snapshot:            snapshot,
		TaskBreakdown:       taskBreakdown,
	}

	return s.enrichWithLLM(cfg, analysis)
}

func buildDisabledAnalysis() model.StabilityAnalysis {
	return model.StabilityAnalysis{
		Enabled:         false,
		GeneratedAt:     time.Now().Format("2006-01-02 15:04:05"),
		Source:          "disabled",
		AbnormalSummary: "稳定性智能分析已关闭，可在系统设置中开启后查看异常摘要、风险评估和处理建议。",
		RiskAssessment: model.RiskAssessment{
			Score:  0,
			Level:  "low",
			Reason: "当前未启用稳定性分析功能。",
		},
		HandlingSuggestions: []string{
			"在系统设置中开启稳定性智能分析。",
			"如需 Agent 增强结论，请同时配置 LLM 接口地址、模型与 API Key。",
		},
	}
}

func scoreTask(res model.MonitorResult, state model.TaskState, unresolvedAlerts int, avgResponse, slowThreshold int64) (int, []string) {
	if slowThreshold <= 0 {
		slowThreshold = 800
	}

	score := 0
	evidence := make([]string, 0, 5)

	redCount := countColor(res.HistoryDots, "red")
	yellowCount := countColor(res.HistoryDots, "yellow")

	switch res.StatusColor {
	case "red":
		score += 55
		evidence = append(evidence, "当前探测结果为故障状态")
	case "yellow":
		score += 28
		evidence = append(evidence, fmt.Sprintf("当前探测结果为慢响应（%s）", res.Duration))
	}

	if state.ConsecutiveFails > 0 {
		score += clamp(state.ConsecutiveFails*8, 0, 24)
		evidence = append(evidence, fmt.Sprintf("连续失败 %d 次", state.ConsecutiveFails))
	}
	if state.IsDown {
		score += 12
		evidence = append(evidence, "任务已进入宕机确认状态")
	}
	if !state.LastAlertTime.IsZero() && time.Since(state.LastAlertTime) < 15*time.Minute {
		score += 8
		evidence = append(evidence, "15 分钟内触发过告警")
	}
	if redCount >= 3 {
		score += 14
		evidence = append(evidence, fmt.Sprintf("最近窗口内出现 %d 次故障点", redCount))
	} else if redCount > 0 {
		score += redCount * 3
	}
	if yellowCount >= 4 {
		score += 10
		evidence = append(evidence, fmt.Sprintf("最近窗口内出现 %d 次慢响应", yellowCount))
	}
	if avgResponse >= slowThreshold && avgResponse > 0 {
		score += 12
		evidence = append(evidence, fmt.Sprintf("平均响应耗时约 %dms", avgResponse))
	}
	if unresolvedAlerts > 0 {
		score += clamp(unresolvedAlerts*8, 0, 16)
		evidence = append(evidence, fmt.Sprintf("存在 %d 条未恢复告警", unresolvedAlerts))
	}

	if len(evidence) == 0 {
		evidence = append(evidence, "最近窗口未发现明显异常")
	}

	return clamp(score, 0, 100), evidence
}

func summarizePerformance(logs []model.PerformanceLog, fallback int64) (avg int64, last int64) {
	if len(logs) == 0 {
		if fallback < 0 {
			fallback = 0
		}
		return fallback, fallback
	}

	var total int64
	last = logs[0].ResponseTime
	for _, item := range logs {
		total += item.ResponseTime
	}
	avg = total / int64(len(logs))
	if last <= 0 && fallback > 0 {
		last = fallback
	}
	return avg, last
}

func scoreOverall(snapshot model.StabilitySnapshot, taskBreakdown []model.StabilityTaskDetail) int {
	if snapshot.TotalTasks == 0 {
		return 0
	}

	score := snapshot.FailedTasks*30 + snapshot.SlowTasks*10 + snapshot.UnresolvedAlerts*8
	for i := 0; i < len(taskBreakdown) && i < 3; i++ {
		score += taskBreakdown[i].RiskScore / 6
	}

	if snapshot.FailedTasks == 0 && snapshot.SlowTasks == 0 && snapshot.UnresolvedAlerts == 0 {
		score = snapshot.RecentRecoveries * 3
	}
	if snapshot.RecentRecoveries > 0 && snapshot.FailedTasks == 0 {
		score -= min(snapshot.RecentRecoveries*2, 10)
	}

	return clamp(score, 0, 100)
}

func riskLevel(score int) string {
	switch {
	case score >= 75:
		return "critical"
	case score >= 55:
		return "high"
	case score >= 30:
		return "medium"
	default:
		return "low"
	}
}

func buildRiskReason(snapshot model.StabilitySnapshot, taskBreakdown []model.StabilityTaskDetail) string {
	parts := make([]string, 0, 4)
	if snapshot.FailedTasks > 0 {
		parts = append(parts, fmt.Sprintf("当前有 %d 个任务处于故障状态", snapshot.FailedTasks))
	}
	if snapshot.SlowTasks > 0 {
		parts = append(parts, fmt.Sprintf("有 %d 个任务出现慢响应", snapshot.SlowTasks))
	}
	if snapshot.UnresolvedAlerts > 0 {
		parts = append(parts, fmt.Sprintf("存在 %d 条未恢复告警", snapshot.UnresolvedAlerts))
	}

	topNames := make([]string, 0, 3)
	for i := 0; i < len(taskBreakdown) && i < 3; i++ {
		if taskBreakdown[i].RiskScore < 30 {
			break
		}
		topNames = append(topNames, taskBreakdown[i].TaskName)
	}
	if len(topNames) > 0 {
		parts = append(parts, "高风险任务集中在："+strings.Join(topNames, "、"))
	}

	if len(parts) == 0 {
		return "当前窗口内未发现明显异常，整体稳定性保持良好。"
	}
	return strings.Join(parts, "；") + "。"
}

func buildLocalSummary(snapshot model.StabilitySnapshot, taskBreakdown []model.StabilityTaskDetail) string {
	if snapshot.TotalTasks == 0 {
		return "当前尚未生成监控结果，建议先完成首轮探测后再查看稳定性分析。"
	}
	if snapshot.FailedTasks == 0 && snapshot.SlowTasks == 0 && snapshot.UnresolvedAlerts == 0 {
		return fmt.Sprintf("当前 %d 个监控任务均保持正常，最近窗口内未发现未恢复告警，整体稳定性良好。", snapshot.TotalTasks)
	}

	topNames := make([]string, 0, 2)
	for i := 0; i < len(taskBreakdown) && i < 2; i++ {
		if taskBreakdown[i].RiskScore == 0 {
			break
		}
		topNames = append(topNames, taskBreakdown[i].TaskName)
	}

	summary := fmt.Sprintf("检测到 %d 个故障任务、%d 个慢响应任务，当前未恢复告警 %d 条。", snapshot.FailedTasks, snapshot.SlowTasks, snapshot.UnresolvedAlerts)
	if len(topNames) > 0 {
		summary += " 需优先关注：" + strings.Join(topNames, "、") + "。"
	}
	if snapshot.RecentRecoveries > 0 {
		summary += fmt.Sprintf(" 最近窗口内已有 %d 次恢复事件。", snapshot.RecentRecoveries)
	}
	return summary
}

func buildSuggestions(snapshot model.StabilitySnapshot, taskBreakdown []model.StabilityTaskDetail) []string {
	suggestions := make([]string, 0, 3)
	if snapshot.TotalTasks == 0 {
		suggestions = appendUnique(suggestions, "先添加关键业务任务并完成首轮探测，以便分析模块形成有效结论。")
		suggestions = appendUnique(suggestions, "优先为核心服务配置健康检查地址和告警阈值，避免后续分析缺少基线。")
		return suggestions
	}

	if snapshot.FailedTasks > 0 && len(taskBreakdown) > 0 {
		top := taskBreakdown[0]
		suggestions = appendUnique(suggestions, fmt.Sprintf("优先处理 [%s]，建议先检查服务进程、网络链路、DNS/证书及最近发布变更。", top.TaskName))
	}
	if snapshot.SlowTasks > 0 {
		suggestions = appendUnique(suggestions, "针对慢响应任务，重点排查上游依赖、数据库慢查询、连接池与线程池饱和情况。")
	}
	if snapshot.UnresolvedAlerts > 0 {
		suggestions = appendUnique(suggestions, "存在未恢复告警，建议建立人工跟踪清单，并复核告警阈值与冷却时间是否合理。")
	}
	if snapshot.FailedTasks == 0 && snapshot.SlowTasks == 0 {
		suggestions = appendUnique(suggestions, "当前整体稳定，建议保持持续观测并定期回顾趋势图与性能日志。")
	}
	if len(suggestions) == 0 {
		suggestions = appendUnique(suggestions, "建议继续观察风险最高的任务，并结合近 30 分钟的发布、网络与资源变更进行复盘。")
	}
	return suggestions[:min(len(suggestions), 3)]
}

func (s *Service) enrichWithLLM(cfg model.AnalysisConfig, current model.StabilityAnalysis) model.StabilityAnalysis {
	llm := cfg.LLM
	if !llm.Enabled || strings.TrimSpace(llm.BaseURL) == "" || strings.TrimSpace(llm.APIKey) == "" || strings.TrimSpace(llm.Model) == "" {
		return current
	}

	promptPayload := struct {
		GeneratedAt      string                      `json:"generated_at"`
		Snapshot         model.StabilitySnapshot     `json:"snapshot"`
		RiskAssessment   model.RiskAssessment        `json:"risk_assessment"`
		TopRiskTasks     []model.StabilityTaskDetail `json:"top_risk_tasks"`
		LocalSummary     string                      `json:"local_summary"`
		LocalSuggestions []string                    `json:"local_suggestions"`
	}{
		GeneratedAt:      current.GeneratedAt,
		Snapshot:         current.Snapshot,
		RiskAssessment:   current.RiskAssessment,
		TopRiskTasks:     current.TaskBreakdown[:min(len(current.TaskBreakdown), 5)],
		LocalSummary:     current.AbnormalSummary,
		LocalSuggestions: current.HandlingSuggestions,
	}

	llmResult, err := s.callLLM(llm, promptPayload)
	if err != nil {
		return current
	}

	if strings.TrimSpace(llmResult.Summary) != "" {
		current.AbnormalSummary = strings.TrimSpace(llmResult.Summary)
	}
	if strings.TrimSpace(llmResult.RiskReason) != "" {
		current.RiskAssessment.Reason = strings.TrimSpace(llmResult.RiskReason)
	}
	if len(llmResult.Suggestions) > 0 {
		current.HandlingSuggestions = normalizeSuggestions(llmResult.Suggestions)
	}
	current.Source = "llm-enhanced"
	return current
}

func (s *Service) callLLM(cfg model.LLMConfig, payload any) (llmNarrative, error) {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return llmNarrative{}, err
	}

	requestBody, err := json.Marshal(llmChatRequest{
		Model:       cfg.Model,
		Temperature: 0.2,
		Messages: []llmChatMessage{
			{
				Role:    "system",
				Content: "你是一名 SRE 稳定性分析 Agent。请根据给定监控快照，用简体中文输出严格 JSON，格式为 {\"summary\":\"...\",\"risk_reason\":\"...\",\"suggestions\":[\"...\",\"...\",\"...\"]}。要求：1）summary 60-120 字；2）risk_reason 只说明风险成因；3）suggestions 最多 3 条且必须可执行；4）不要输出 Markdown、代码块或额外字段。",
			},
			{
				Role:    "user",
				Content: string(body),
			},
		},
	})
	if err != nil {
		return llmNarrative{}, err
	}

	timeout := cfg.TimeoutSeconds
	if timeout <= 0 {
		timeout = 20
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(cfg.BaseURL), bytes.NewReader(requestBody))
	if err != nil {
		return llmNarrative{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return llmNarrative{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		raw, _ := io.ReadAll(resp.Body)
		return llmNarrative{}, fmt.Errorf("llm request failed: %s", strings.TrimSpace(string(raw)))
	}

	var parsed llmChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return llmNarrative{}, err
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return llmNarrative{}, fmt.Errorf("%s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return llmNarrative{}, fmt.Errorf("llm response has no choices")
	}

	content := compactJSONBlock(parsed.Choices[0].Message.Content)
	var out llmNarrative
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return llmNarrative{}, err
	}
	out.Suggestions = normalizeSuggestions(out.Suggestions)
	return out, nil
}

func normalizeSuggestions(in []string) []string {
	out := make([]string, 0, 3)
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = appendUnique(out, item)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func compactJSONBlock(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return text
}

func cloneAnalysis(in model.StabilityAnalysis) model.StabilityAnalysis {
	out := in
	out.HandlingSuggestions = append([]string(nil), in.HandlingSuggestions...)
	out.TaskBreakdown = make([]model.StabilityTaskDetail, len(in.TaskBreakdown))
	for i, task := range in.TaskBreakdown {
		out.TaskBreakdown[i] = task
		out.TaskBreakdown[i].Evidence = append([]string(nil), task.Evidence...)
	}
	return out
}

func countColor(history []string, color string) int {
	count := 0
	for _, item := range history {
		if item == color {
			count++
		}
	}
	return count
}

func appendUnique(list []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return list
	}
	for _, item := range list {
		if item == text {
			return list
		}
	}
	return append(list, text)
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

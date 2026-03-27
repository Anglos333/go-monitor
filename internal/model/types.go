package model

import (
	"time"

	"gorm.io/gorm"
)

// Config 表示系统的完整配置，包含监控间隔、告警阈值、SMTP 设置以及监控任务列表。
type Config struct {
	Interval       int            `json:"interval"`
	AlertThreshold int            `json:"alert_threshold"`
	AlertCooldown  int            `json:"alert_cooldown"`
	NextTaskID     int            `json:"next_task_id"` // 全局自增发号器
	SMTP           SMTPConfig     `json:"smtp"`
	Analysis       AnalysisConfig `json:"analysis"`
	Tasks          []MonitorTask  `json:"tasks"`
}

// SMTPConfig 包含邮件服务器连接信息及收件人地址。
type SMTPConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	To       string `json:"to"` // 收件人邮箱，多个可用逗号分隔
}

// AnalysisConfig 定义稳定性智能分析模块的开关、缓存与 LLM 增强配置。
type AnalysisConfig struct {
	Enabled               bool      `json:"enabled"`
	CacheSeconds          int       `json:"cache_seconds"`
	DetailEventLimit      int       `json:"detail_event_limit"`
	PerformanceSampleSize int       `json:"performance_sample_size"`
	SlowThresholdMS       int64     `json:"slow_threshold_ms"`
	LLM                   LLMConfig `json:"llm"`
}

// LLMConfig 定义外部大模型接口连接参数，采用 OpenAI 兼容的 Chat Completions 协议。
type LLMConfig struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// MonitorResult 用于 Web 页面展示的监控结果视图模型，聚合了最新检查信息和历史状态。
type MonitorTask struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Starred bool   `json:"starred"` // 是否标星置顶
}

type MonitorResult struct {
	ID          int
	TaskName    string
	URL         string
	StatusCode  int
	Duration    string // 响应时间格式化字符串（如 "123ms"）
	DurationInt int64  // 响应时间原始毫秒数，用于排序
	Status      string // 状态描述（如 "正常"、"失败"）
	StatusColor string // 前端颜色标识
	IsSuccess   bool
	LastUpdate  string   // 上次检查时间格式化字符串
	HistoryDots []string // 历史状态点阵，用于图表显示
	Starred     bool     // 传递给前端的标星状态
}

// TaskState 用于内部维护每个任务的动态状态（失败计数、上次告警时间、是否宕机）。
type TaskState struct {
	ConsecutiveFails int
	LastAlertTime    time.Time
	IsDown           bool
}

// EventLog 记录系统重要事件（如告警触发、恢复），用于历史追溯。
type EventLog struct {
	gorm.Model
	TaskName   string
	EventTime  string // 事件发生时间（格式化）
	Type       string // 事件类型（如 "alert", "recover"）
	Message    string
	IsResolved bool // 标记告警是否已解除
}

// PerformanceLog 记录每次检查的响应时间，用于性能趋势分析。
type PerformanceLog struct {
	gorm.Model
	TaskID       int
	TaskName     string
	ResponseTime int64  // 响应时间（毫秒）
	CheckTime    string // 检查时间（格式化）
}

// StabilityAnalysis 表示稳定性分析模块的统一输出结构。
type StabilityAnalysis struct {
	Enabled             bool                  `json:"enabled"`
	GeneratedAt         string                `json:"generated_at"`
	Source              string                `json:"source"`
	AbnormalSummary     string                `json:"abnormal_summary"`
	RiskAssessment      RiskAssessment        `json:"risk_assessment"`
	HandlingSuggestions []string              `json:"handling_suggestions"`
	Snapshot            StabilitySnapshot     `json:"snapshot"`
	TaskBreakdown       []StabilityTaskDetail `json:"task_breakdown"`
}

// RiskAssessment 表示当前系统稳定性风险的分值、等级与原因说明。
type RiskAssessment struct {
	Score  int    `json:"score"`
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

// StabilitySnapshot 表示用于卡片与详情面板展示的聚合监控快照。
type StabilitySnapshot struct {
	TotalTasks       int `json:"total_tasks"`
	HealthyTasks     int `json:"healthy_tasks"`
	SlowTasks        int `json:"slow_tasks"`
	FailedTasks      int `json:"failed_tasks"`
	UnresolvedAlerts int `json:"unresolved_alerts"`
	RecentRecoveries int `json:"recent_recoveries"`
}

// StabilityTaskDetail 表示单个任务的风险拆解结果。
type StabilityTaskDetail struct {
	TaskID           int      `json:"task_id"`
	TaskName         string   `json:"task_name"`
	Status           string   `json:"status"`
	StatusColor      string   `json:"status_color"`
	RiskScore        int      `json:"risk_score"`
	RiskLevel        string   `json:"risk_level"`
	FailureStreak    int      `json:"failure_streak"`
	AvgResponseMS    int64    `json:"avg_response_ms"`
	LastResponseMS   int64    `json:"last_response_ms"`
	UnresolvedAlerts int      `json:"unresolved_alerts"`
	Evidence         []string `json:"evidence"`
}

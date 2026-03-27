// Package repository 提供数据持久化能力，使用 GORM 操作 SQLite 数据库，
// 包含事件日志和性能日志的存储与查询。
package repository

import (
	"monitor/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Repo 封装了数据库连接，并提供操作日志表的方法。
type Repo struct {
	DB *gorm.DB
}

// Close 关闭底层数据库连接。
func (r *Repo) Close() error {
	sqlDB, err := r.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// New 初始化 SQLite 数据库连接，并自动迁移 EventLog 和 PerformanceLog 表。
func New(path string) (*Repo, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&model.EventLog{}, &model.PerformanceLog{}); err != nil {
		return nil, err
	}
	return &Repo{DB: db}, nil
}

// CreateEvent 保存一条事件日志。
func (r *Repo) CreateEvent(e *model.EventLog) {
	r.DB.Create(e)
}

// ResolveDownEvents 将指定任务的所有未解决的宕机事件标记为已解决。
func (r *Repo) ResolveDownEvents(taskName string) {
	r.DB.Model(&model.EventLog{}).
		Where("task_name = ? AND type = ? AND is_resolved = ?", taskName, "🔥 宕机警告", false).
		Update("is_resolved", true)
}

// QueryOpenAlerts 返回当前所有尚未恢复的宕机告警。
func (r *Repo) QueryOpenAlerts() []model.EventLog {
	var logs []model.EventLog
	r.DB.Where("type = ? AND is_resolved = ?", "🔥 宕机警告", false).
		Order("id desc").
		Find(&logs)
	return logs
}

// CreatePerformance 保存一条性能日志。
func (r *Repo) CreatePerformance(p *model.PerformanceLog) {
	r.DB.Create(p)
}

// QueryPerformance 查询指定任务的最近 limit 条性能日志，按 ID 降序返回。
func (r *Repo) QueryPerformance(taskID, limit int) []model.PerformanceLog {
	var logs []model.PerformanceLog
	q := r.DB.Order("id desc")
	if taskID > 0 {
		q = q.Where("task_id = ?", taskID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	q.Find(&logs)
	return logs
}

// QueryEvents 查询最近的事件日志，limit 指定返回条数，为 0 时返回所有。
func (r *Repo) QueryEvents(limit int) []model.EventLog {
	var logs []model.EventLog
	q := r.DB.Order("id desc")
	if limit > 0 {
		q = q.Limit(limit)
	}
	q.Find(&logs)
	return logs
}

// ClearLogs 清空事件日志表和性能日志表。
func (r *Repo) ClearLogs() {
	r.DB.Exec("DELETE FROM event_logs")
	r.DB.Exec("DELETE FROM performance_logs")
}

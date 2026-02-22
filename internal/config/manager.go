package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"monitor/internal/model"
)

type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  model.Config
}

func NewManager(path string) *Manager {
	return &Manager{path: path}
}

// LoadOrDefault 加载配置文件，若文件不存在则生成默认配置并保存。
// 默认配置包含一个示例任务（百度）和合理的监控参数。
func (m *Manager) LoadOrDefault() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if err != nil {
		m.cfg = model.Config{
			Interval:       5,
			AlertThreshold: 3,
			AlertCooldown:  60,
			Tasks: []model.MonitorTask{
				{ID: 1, Name: "百度搜索", URL: "https://www.baidu.com"},
			},
		}
		return m.saveLocked()
	}
	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return err
	}
	if m.cfg.Interval <= 0 {
		m.cfg.Interval = 5
	}
	if m.cfg.AlertThreshold <= 0 {
		m.cfg.AlertThreshold = 3
	}
	if m.cfg.AlertCooldown < 0 {
		m.cfg.AlertCooldown = 60
	}
	return nil
}

func (m *Manager) Get() model.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// AddTask 添加一个新的监控任务。
// 参数 name 和 rawURL 会自动去除首尾空格，并补全默认 https 前缀。
// 对 URL 进行格式校验、域名解析检查，确保任务可用性。
// 返回生成的任务对象（包含自动分配的 ID）或错误。
func (m *Manager) AddTask(name, rawURL string) (model.MonitorTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	if name == "" || rawURL == "" {
		return model.MonitorTask{}, fmt.Errorf("name/url 不能为空")
	}

	// // 默认添加 https 前缀
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return model.MonitorTask{}, fmt.Errorf("URL 格式不合法: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return model.MonitorTask{}, fmt.Errorf("仅支持 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return model.MonitorTask{}, fmt.Errorf("URL 缺少主机名")
	}

	// 防止无意义输入，对域名进行简单检查（如果是IP则跳过DNS解析）
	if net.ParseIP(host) == nil {
		if !strings.Contains(host, ".") && host != "localhost" {
			return model.MonitorTask{}, fmt.Errorf("域名不合法，请输入完整域名")
		}
		if _, err := net.LookupHost(host); err != nil {
			return model.MonitorTask{}, fmt.Errorf("域名无法解析: %s", host)
		}
	}

	// 生成新ID（当前最大ID+1）
	maxID := 0
	for _, t := range m.cfg.Tasks {
		if t.ID > maxID {
			maxID = t.ID
		}
	}
	task := model.MonitorTask{
		ID:   maxID + 1,
		Name: name,
		URL:  rawURL,
	}
	m.cfg.Tasks = append(m.cfg.Tasks, task)
	return task, m.saveLocked()
}

func (m *Manager) DeleteTask(id int) (deletedURL string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var newTasks []model.MonitorTask
	for _, t := range m.cfg.Tasks {
		if t.ID == id {
			deletedURL = t.URL
			continue
		}
		newTasks = append(newTasks, t)
	}
	m.cfg.Tasks = newTasks
	return deletedURL, m.saveLocked()
}

// UpdateSettings 更新监控全局设置（间隔、阈值、冷却时间、SMTP配置）。
// 对传入的数值进行合法性校验，并保留原有SMTP密码（若新密码为空字符串）。
func (m *Manager) UpdateSettings(in model.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if in.Interval <= 0 {
		in.Interval = 5
	}
	if in.AlertThreshold <= 0 {
		in.AlertThreshold = 3
	}
	if in.AlertCooldown < 0 {
		in.AlertCooldown = 60
	}
	// 如果新密码为空，则保留原有密码（避免前端传空覆盖）
	if strings.TrimSpace(in.SMTP.Password) == "" {
		in.SMTP.Password = m.cfg.SMTP.Password
	}

	m.cfg.Interval = in.Interval
	m.cfg.AlertThreshold = in.AlertThreshold
	m.cfg.AlertCooldown = in.AlertCooldown
	m.cfg.SMTP = in.SMTP

	return m.saveLocked()
}

// saveLocked 将当前配置以JSON格式写入文件，调用前需持有锁。
func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0644)
}

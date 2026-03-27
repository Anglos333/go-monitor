package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"

	"monitor/internal/model"
)

// 🔥 AES 密钥来源：环境变量 MONITOR_SECRET_KEY（推荐），未提供则使用兼容的默认值。
// 为兼容历史密文，默认值保持不变；生产环境请务必设置 MONITOR_SECRET_KEY。
var secretKey = loadSecretKey()

func loadSecretKey() []byte {
	raw := os.Getenv("MONITOR_SECRET_KEY")
	if raw == "" {
		raw = "HakimiMonitorKey1234567890123456"
	}
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  model.Config
}

// ResetToExample 用 config.example.json 覆盖当前配置，并返回新配置。
// 调用方应在外层加额外校验（如密码确认）。
func (m *Manager) ResetToExample(examplePath string) (model.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(examplePath)
	if err != nil {
		return model.Config{}, err
	}
	var cfg model.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return model.Config{}, err
	}
	applyConfigDefaults(&cfg)

	// 密码是明文存储在内存，落盘时会加密
	m.cfg = cfg
	if err := m.saveLocked(); err != nil {
		return model.Config{}, err
	}
	return cfg, nil
}

func NewManager(path string) *Manager {
	return &Manager{path: path}
}

// 🔥 通用加密函数
func encryptSecret(text string) string {
	if text == "" {
		return ""
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return text // 加密失败直接返回原值
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return text
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return text
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(text), nil)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

// 🔥 通用解密函数：若密文损坏或被篡改，则返回错误并拒绝继续加载配置。
func decryptSecret(cryptoText, fieldName string) (string, error) {
	if cryptoText == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return "", fmt.Errorf("%s不是有效密文，请通过系统设置重新保存配置", fieldName)
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("%s密文长度非法，配置可能已损坏", fieldName)
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("%s解密失败，配置可能已损坏或被篡改", fieldName)
	}
	return string(plaintext), nil
}

func encryptPassword(text string) string {
	return encryptSecret(text)
}

func decryptPassword(cryptoText string) (string, error) {
	return decryptSecret(cryptoText, "SMTP 密码")
}

func encryptAPIKey(text string) string {
	return encryptSecret(text)
}

func decryptAPIKey(cryptoText string) (string, error) {
	return decryptSecret(cryptoText, "LLM API Key")
}

func (m *Manager) LoadOrDefault() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if err != nil {
		m.cfg = model.Config{
			Interval:       5,
			AlertThreshold: 3,
			AlertCooldown:  60,
			Analysis: model.AnalysisConfig{
				Enabled:               true,
				CacheSeconds:          60,
				DetailEventLimit:      20,
				PerformanceSampleSize: 10,
				SlowThresholdMS:       800,
				LLM: model.LLMConfig{
					BaseURL:        "https://api.openai.com/v1/chat/completions",
					Model:          "gpt-4o-mini",
					TimeoutSeconds: 20,
				},
			},
			Tasks: []model.MonitorTask{
				{ID: 1, Name: "百度搜索", URL: "https://www.baidu.com"},
			},
		}
		applyConfigDefaults(&m.cfg)
		return m.saveLocked()
	}
	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return err
	}

	// 🔥 读取时，将密文还原成明文供系统内部使用；解密失败则拒绝加载。
	password, err := decryptPassword(m.cfg.SMTP.Password)
	if err != nil {
		return err
	}
	m.cfg.SMTP.Password = password

	apiKey, err := decryptAPIKey(m.cfg.Analysis.LLM.APIKey)
	if err != nil {
		return err
	}
	m.cfg.Analysis.LLM.APIKey = apiKey

	applyConfigDefaults(&m.cfg)
	return nil

}

func (m *Manager) Get() model.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// NormalizeAndValidateTaskInput 统一规范化并校验监控任务输入。
// 对未带协议的地址统一补全为 http://，并校验 URL、主机名和可解析性。
func NormalizeAndValidateTaskInput(name, rawURL string) (string, string, error) {
	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	if name == "" || rawURL == "" {
		return "", "", fmt.Errorf("name/url 不能为空")
	}

	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "http://" + rawURL
	}

	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("URL 格式不合法: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("仅支持 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("URL 缺少主机名")
	}

	if net.ParseIP(host) == nil {
		if !strings.Contains(host, ".") && host != "localhost" {
			return "", "", fmt.Errorf("域名不合法，请输入完整域名")
		}
		if _, err := net.LookupHost(host); err != nil {
			return "", "", fmt.Errorf("域名无法解析: %s", host)
		}
	}

	return name, rawURL, nil
}

func (m *Manager) AddTask(name, rawURL string) (model.MonitorTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var err error
	name, rawURL, err = NormalizeAndValidateTaskInput(name, rawURL)
	if err != nil {
		return model.MonitorTask{}, err
	}

	// 直接用发号器的号码创建任务
	task := model.MonitorTask{
		ID:   m.cfg.NextTaskID, // 🔥 从全局发号器取号
		Name: name,
		URL:  rawURL,
	}

	m.cfg.NextTaskID++ // 🔥 发号器自增（永远向前，绝不回头！）
	m.cfg.Tasks = append(m.cfg.Tasks, task)
	return task, m.saveLocked()
}

// UpdateTask 修改现有监控任务，返回更新后的任务和旧 URL（供上层清理缓存使用）。
func (m *Manager) UpdateTask(id int, name, rawURL string) (model.MonitorTask, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id <= 0 {
		return model.MonitorTask{}, "", fmt.Errorf("invalid id")
	}

	var err error
	name, rawURL, err = NormalizeAndValidateTaskInput(name, rawURL)
	if err != nil {
		return model.MonitorTask{}, "", err
	}

	for i := range m.cfg.Tasks {
		if m.cfg.Tasks[i].ID == id {
			oldURL := m.cfg.Tasks[i].URL
			m.cfg.Tasks[i].Name = name
			m.cfg.Tasks[i].URL = rawURL
			if err := m.saveLocked(); err != nil {
				return model.MonitorTask{}, "", err
			}
			return m.cfg.Tasks[i], oldURL, nil
		}
	}

	return model.MonitorTask{}, "", fmt.Errorf("未找到指定任务")
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

	if strings.TrimSpace(in.SMTP.Password) == "" {
		in.SMTP.Password = m.cfg.SMTP.Password
	}
	if in.Analysis.CacheSeconds <= 0 {
		in.Analysis.CacheSeconds = m.cfg.Analysis.CacheSeconds
	}
	if in.Analysis.DetailEventLimit <= 0 {
		in.Analysis.DetailEventLimit = m.cfg.Analysis.DetailEventLimit
	}
	if in.Analysis.PerformanceSampleSize <= 0 {
		in.Analysis.PerformanceSampleSize = m.cfg.Analysis.PerformanceSampleSize
	}
	if in.Analysis.SlowThresholdMS <= 0 {
		in.Analysis.SlowThresholdMS = m.cfg.Analysis.SlowThresholdMS
	}
	if strings.TrimSpace(in.Analysis.LLM.BaseURL) == "" {
		in.Analysis.LLM.BaseURL = m.cfg.Analysis.LLM.BaseURL
	}
	if strings.TrimSpace(in.Analysis.LLM.Model) == "" {
		in.Analysis.LLM.Model = m.cfg.Analysis.LLM.Model
	}
	if in.Analysis.LLM.TimeoutSeconds <= 0 {
		in.Analysis.LLM.TimeoutSeconds = m.cfg.Analysis.LLM.TimeoutSeconds
	}
	if strings.TrimSpace(in.Analysis.LLM.APIKey) == "" {
		in.Analysis.LLM.APIKey = m.cfg.Analysis.LLM.APIKey
	}
	normalizeAnalysisConfig(&in.Analysis)

	m.cfg.Interval = in.Interval
	m.cfg.AlertThreshold = in.AlertThreshold
	m.cfg.AlertCooldown = in.AlertCooldown
	m.cfg.SMTP = in.SMTP
	m.cfg.Analysis = in.Analysis

	return m.saveLocked()
}

// saveLocked 将当前配置以JSON格式写入文件，调用前需持有锁。
func (m *Manager) saveLocked() error {
	// 🔥 核心：因为 m.cfg 在内存里是明文的（为了方便发送邮件），
	// 在保存到硬盘前，我们“克隆”一份配置，并把克隆体里的密码加密。
	saveCfg := m.cfg
	saveCfg.SMTP.Password = encryptPassword(m.cfg.SMTP.Password)
	saveCfg.Analysis.LLM.APIKey = encryptAPIKey(m.cfg.Analysis.LLM.APIKey)

	data, err := json.MarshalIndent(saveCfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0644)
}

// 切换任务的标星状态，返回最新状态（true 表示已标星）
func (m *Manager) ToggleStar(id int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, t := range m.cfg.Tasks {
		if t.ID == id {
			m.cfg.Tasks[i].Starred = !t.Starred
			return m.cfg.Tasks[i].Starred, m.saveLocked()
		}
	}
	return false, fmt.Errorf("未找到指定任务")
}

func applyConfigDefaults(cfg *model.Config) {
	if cfg.Interval <= 0 {
		cfg.Interval = 5
	}
	if cfg.AlertThreshold <= 0 {
		cfg.AlertThreshold = 3
	}
	if cfg.AlertCooldown < 0 {
		cfg.AlertCooldown = 60
	}
	if cfg.NextTaskID <= 0 {
		maxID := 0
		for _, t := range cfg.Tasks {
			if t.ID > maxID {
				maxID = t.ID
			}
		}
		cfg.NextTaskID = maxID + 1
	}
	normalizeAnalysisConfig(&cfg.Analysis)
}

func normalizeAnalysisConfig(analysis *model.AnalysisConfig) {
	if !analysis.Enabled && !analysis.LLM.Enabled && analysis.CacheSeconds == 0 && analysis.DetailEventLimit == 0 && analysis.PerformanceSampleSize == 0 && analysis.SlowThresholdMS == 0 && analysis.LLM.TimeoutSeconds == 0 && strings.TrimSpace(analysis.LLM.BaseURL) == "" && strings.TrimSpace(analysis.LLM.Model) == "" && strings.TrimSpace(analysis.LLM.APIKey) == "" {
		analysis.Enabled = true
	}
	if analysis.CacheSeconds <= 0 {
		analysis.CacheSeconds = 60
	}
	if analysis.DetailEventLimit <= 0 {
		analysis.DetailEventLimit = 20
	}
	if analysis.PerformanceSampleSize <= 0 {
		analysis.PerformanceSampleSize = 10
	}
	if analysis.SlowThresholdMS <= 0 {
		analysis.SlowThresholdMS = 800
	}
	if strings.TrimSpace(analysis.LLM.BaseURL) == "" {
		analysis.LLM.BaseURL = "https://api.openai.com/v1/chat/completions"
	}
	if strings.TrimSpace(analysis.LLM.Model) == "" {
		analysis.LLM.Model = "gpt-4o-mini"
	}
	if analysis.LLM.TimeoutSeconds <= 0 {
		analysis.LLM.TimeoutSeconds = 20
	}
}

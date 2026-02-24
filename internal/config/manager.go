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

	// 兼容初始化逻辑
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

// 🔥 加密函数
func encryptPassword(text string) string {
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

// 🔥 解密函数
func decryptPassword(cryptoText string) string {
	if cryptoText == "" {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return cryptoText // 不是base64格式，说明是明文，直接返回原值（向下兼容）
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return cryptoText
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return cryptoText
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return cryptoText // 数据长度不对，说明是明文
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return cryptoText // 解密失败，说明是明文
	}
	return string(plaintext)
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
			Tasks: []model.MonitorTask{
				{ID: 1, Name: "百度搜索", URL: "https://www.baidu.com"},
			},
		}
		return m.saveLocked()
	}
	if err := json.Unmarshal(data, &m.cfg); err != nil {
		return err
	}

	// 🔥 读取时，将密文还原成明文供系统内部使用
	m.cfg.SMTP.Password = decryptPassword(m.cfg.SMTP.Password)

	if m.cfg.Interval <= 0 {
		m.cfg.Interval = 5
	}
	if m.cfg.AlertThreshold <= 0 {
		m.cfg.AlertThreshold = 3
	}
	if m.cfg.AlertCooldown < 0 {
		m.cfg.AlertCooldown = 60
	}
	// 兼容旧配置文件，初始化发号器
	if m.cfg.NextTaskID <= 0 {
		maxID := 0
		for _, t := range m.cfg.Tasks {
			if t.ID > maxID {
				maxID = t.ID
			}
		}
		m.cfg.NextTaskID = maxID + 1 // 把发号器拨到最大值的下一位
	}
	return nil

}

func (m *Manager) Get() model.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) AddTask(name, rawURL string) (model.MonitorTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	if name == "" || rawURL == "" {
		return model.MonitorTask{}, fmt.Errorf("name/url 不能为空")
	}

	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "http://" + rawURL
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

	if net.ParseIP(host) == nil {
		if !strings.Contains(host, ".") && host != "localhost" {
			return model.MonitorTask{}, fmt.Errorf("域名不合法，请输入完整域名")
		}
		if _, err := net.LookupHost(host); err != nil {
			return model.MonitorTask{}, fmt.Errorf("域名无法解析: %s", host)
		}
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

	m.cfg.Interval = in.Interval
	m.cfg.AlertThreshold = in.AlertThreshold
	m.cfg.AlertCooldown = in.AlertCooldown
	m.cfg.SMTP = in.SMTP

	return m.saveLocked()
}

// saveLocked 将当前配置以JSON格式写入文件，调用前需持有锁。
func (m *Manager) saveLocked() error {
	// 🔥 核心：因为 m.cfg 在内存里是明文的（为了方便发送邮件），
	// 在保存到硬盘前，我们“克隆”一份配置，并把克隆体里的密码加密。
	saveCfg := m.cfg
	saveCfg.SMTP.Password = encryptPassword(m.cfg.SMTP.Password)

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

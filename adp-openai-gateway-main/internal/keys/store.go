package keys

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ApiKey 网关分配的 API Key
type ApiKey struct {
	Key       string `json:"key"`        // sk-yilan-xxx
	Name      string `json:"name"`       // 使用人/用途名
	Note      string `json:"note"`       // 备注
	Enabled   bool   `json:"enabled"`    // 启用状态
	CreatedAt string `json:"created_at"` // RFC3339
}

// Store 基于 JSON 文件的 key 存储（增删改立即落盘、无需重启生效）
type Store struct {
	mu   sync.RWMutex
	path string
	keys []ApiKey
}

// NewStore 加载 key 文件；不存在则创建空存储
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := s.saveLocked([]ApiKey{}); err != nil {
				return nil, err
			}
			return s, nil
		}
		return nil, fmt.Errorf("读取 key 文件失败: %w", err)
	}
	var file struct {
		Keys []ApiKey `json:"keys"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("解析 key 文件失败: %w", err)
	}
	if file.Keys == nil {
		file.Keys = []ApiKey{}
	}
	s.keys = file.Keys
	return s, nil
}

// ImportEnvKeys 一次性迁移：把 .env GATEWAY_KEYS 里的 key 导入存储（来源标记在备注）。
// 返回导入数量；仅当存储为空时执行，此后 .env 不再生效。
func (s *Store) ImportEnvKeys(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keys) > 0 {
		return 0
	}
	now := time.Now().Format(time.RFC3339)
	n := 0
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s.keys = append(s.keys, ApiKey{
			Key: k, Name: "env迁移", Note: "原 GATEWAY_KEYS 配置迁移，可在页面编辑",
			Enabled: true, CreatedAt: now,
		})
		n++
	}
	if n > 0 {
		if err := s.persistLocked(); err != nil {
			log.Printf("[KeyStore] env迁移写入失败: %v", err)
			return 0
		}
		log.Printf("[KeyStore] 已从 GATEWAY_KEYS 迁移 %d 个 key，此后 .env 的 GATEWAY_KEYS 不再生效", n)
	}
	return n
}

func (s *Store) saveLocked(keys []ApiKey) error {
	out, _ := json.MarshalIndent(map[string]interface{}{"keys": keys}, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) persistLocked() error {
	return s.saveLocked(s.keys)
}

// List 返回副本列表
func (s *Store) List() []ApiKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ApiKey, len(s.keys))
	copy(out, s.keys)
	return out
}

// Validate 鉴权：key 存在且启用
func (s *Store) Validate(key string) bool {
	if key == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.keys {
		if k.Key == key {
			return k.Enabled
		}
	}
	return false
}

// GenerateKey 生成 sk-gw- 前缀的随机 key
func GenerateKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "sk-gw-" + hex.EncodeToString(b)
}

// Add 新增 key
func (s *Store) Add(name, note string) (ApiKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ApiKey{}, fmt.Errorf("请填写名称")
	}
	k := ApiKey{
		Key:       GenerateKey(),
		Name:      name,
		Note:      strings.TrimSpace(note),
		Enabled:   true,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 防御性查重
	for _, exist := range s.keys {
		if exist.Key == k.Key {
			return ApiKey{}, fmt.Errorf("生成 key 重复，请重试")
		}
	}
	s.keys = append(s.keys, k)
	if err := s.persistLocked(); err != nil {
		return ApiKey{}, err
	}
	return k, nil
}

// Update 编辑名称/备注/启停（传 nil 表示不修改该项），返回是否找到
func (s *Store) Update(key string, name, note *string, enabled *bool) (ApiKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].Key == key {
			if name != nil {
				s.keys[i].Name = strings.TrimSpace(*name)
			}
			if note != nil {
				s.keys[i].Note = strings.TrimSpace(*note)
			}
			if enabled != nil {
				s.keys[i].Enabled = *enabled
			}
			if err := s.persistLocked(); err != nil {
				log.Printf("[KeyStore] 保存失败: %v", err)
				return s.keys[i], true
			}
			return s.keys[i], true
		}
	}
	return ApiKey{}, false
}

// Delete 删除 key
func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].Key == key {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			if err := s.persistLocked(); err != nil {
				log.Printf("[KeyStore] 保存失败: %v", err)
			}
			return true
		}
	}
	return false
}

// FilePath 返回存储文件路径（日志展示用）
func (s *Store) FilePath() string { return filepath.Clean(s.path) }

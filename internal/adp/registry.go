package adp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ========== 腾讯云 ADP 管理 API（TC3-HMAC-SHA256 签名）==========

const (
	adpAPIHost    = "adp.tencentcloudapi.com"
	adpAPIURL     = "https://adp.tencentcloudapi.com"
	adpService    = "adp"
	adpAPIVersion = "2026-05-20"
)

// tc3Sign 按 TC3-HMAC-SHA256 构造管理 API 请求头
// （按腾讯云 TC3-HMAC-SHA256 官方签名规范实现）
func tc3Sign(secretId, secretKey, action, region, payload string) (http.Header, error) {
	now := time.Now().UTC()
	timestamp := now.Unix()
	date := now.Format("2006-01-02")

	canonicalHeaders := "content-type:application/json; charset=utf-8\n" +
		"host:" + adpAPIHost + "\n" +
		"x-tc-action:" + strings.ToLower(action) + "\n" +
		"x-tc-region:" + region + "\n" +
		"x-tc-timestamp:" + fmt.Sprintf("%d", timestamp) + "\n" +
		"x-tc-version:" + adpAPIVersion + "\n"
	signedHeaders := "content-type;host;x-tc-action;x-tc-region;x-tc-timestamp;x-tc-version"

	hashedPayload := sha256.Sum256([]byte(payload))
	// 注意：canonicalHeaders 结尾自带换行后需再补一个空行再接 signedHeaders，
	// 与运营端 tencent_adp.py 生产可用实现逐字节一致，否则报 AuthFailure.SignatureFailure
	canonicalRequest := "POST\n/\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + hex.EncodeToString(hashedPayload[:])

	credentialScope := date + "/" + adpService + "/tc3_request"
	hashedCanonical := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "TC3-HMAC-SHA256\n" +
		fmt.Sprintf("%d", timestamp) + "\n" +
		credentialScope + "\n" +
		hex.EncodeToString(hashedCanonical[:])

	sign := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	secretDate := sign([]byte("TC3"+secretKey), date)
	secretService := sign(secretDate, adpService)
	secretSigning := sign(secretService, "tc3_request")
	signature := hex.EncodeToString(sign(secretSigning, stringToSign))

	auth := "TC3-HMAC-SHA256 " +
		"Credential=" + secretId + "/" + credentialScope + ", " +
		"SignedHeaders=" + signedHeaders + ", " +
		"Signature=" + signature

	header := http.Header{}
	header.Set("Authorization", auth)
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Host", adpAPIHost)
	header.Set("X-TC-Action", action)
	header.Set("X-TC-Region", region)
	header.Set("X-TC-Timestamp", fmt.Sprintf("%d", timestamp))
	header.Set("X-TC-Version", adpAPIVersion)
	return header, nil
}

// callADPAPI 调用 ADP 管理 API（POST + TC3 签名）
func callADPAPI(secretId, secretKey, action, region string, params map[string]interface{}, result interface{}) error {
	payloadBytes, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	payload := string(payloadBytes)

	header, err := tc3Sign(secretId, secretKey, action, region, payload)
	if err != nil {
		return fmt.Errorf("签名失败: %w", err)
	}

	req, err := http.NewRequest("POST", adpAPIURL, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构建请求失败: %w", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求ADP管理API失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var wrapped struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
			RequestId string `json:"RequestId"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return fmt.Errorf("ADP管理API返回HTTP %d，解析失败: %s", resp.StatusCode, truncate(string(body), 300))
	}
	if wrapped.Response.Error != nil {
		return fmt.Errorf("ADP管理API错误: %s - %s", wrapped.Response.Error.Code, wrapped.Response.Error.Message)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("解析ADP管理API响应失败: %w", err)
	}
	return nil
}

// ========== 模型白名单注册表 ==========

// Model ADP 可用模型（DescribeModelList，ModelScene=18 为 Claw 模式可换清单）
type Model struct {
	ModelId    string `json:"model_id"`    // 如 "TCADP/glm-5"
	ModelName  string `json:"model_name"`  // 如 "glm-5"
	Name       string `json:"name"`        // 展示名
	Developer  string `json:"developer"`   // 模型作者
	Provider   string `json:"provider"`    // 供应方
	ContextLen string `json:"context_len"` // 上下文长度描述
}

// bareName 去掉 ModelId 的 "厂商/" 前缀（如 TCADP/glm-5.3-flashx → glm-5.3-flashx）。
// 实测对话接口 ModelName 参数接受裸名与全前缀两种写法，裸名作为对外展示与传参口径。
func bareName(id string) string {
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// ChatModelName 返回对话接口 ModelName 参数应传的值：裸名（ModelName 缺失时取 ModelId 去前缀）
func (m Model) ChatModelName() string {
	if m.ModelName != "" {
		return m.ModelName
	}
	return bareName(m.ModelId)
}

// DisplayName 返回 /v1/models 列表展示用的 id（裸名）
func (m Model) DisplayName() string {
	return m.ChatModelName()
}

// builtinClawModels 内置静态模型清单（DescribeModelList ModelScene=18，2026-11-13 实测拉取快照）。
// 未配置 ADP_SPACE_ID 或 DescribeModelList 调用失败时兜底使用；建议配置 SpaceId 走实时拉取。
var builtinClawModels = []Model{
	{ModelId: "Auto/auto", Name: "Auto（自动选优）", Developer: "腾讯云"},
	{ModelId: "Hunyuan/hy4", Name: "Tencent Hy4", Developer: "腾讯混元"},
	{ModelId: "Hunyuan/hy4-preview", Name: "Tencent Hy4 preview", Developer: "腾讯混元"},
	{ModelId: "Hunyuan/hy3", Name: "Tencent Hy3", Developer: "腾讯混元"},
	{ModelId: "TCADP/kimi-k3.1-pro", Name: "Kimi K3.1 Pro", Developer: "月之暗面"},
	{ModelId: "TCADP/kimi-k2.8-preview", Name: "Kimi K2.8 Preview", Developer: "月之暗面"},
	{ModelId: "TCADP/kimi-k3", Name: "Kimi K3", Developer: "月之暗面"},
	{ModelId: "TCADP/glm-5.3-flashx", Name: "智谱 GLM-5.3-FlashX", Developer: "智谱 AI"},
	{ModelId: "TCADP/glm-5.3-flash", Name: "智谱 GLM-5.3-Flash", Developer: "智谱 AI"},
	{ModelId: "TCADP/glm-5.3", Name: "智谱 GLM-5.3", Developer: "智谱 AI"},
	{ModelId: "TCADP/glm-5.2", Name: "智谱GLM-5.2", Developer: "智谱 AI"},
	{ModelId: "TCADP/minimax-m3", Name: "MiniMax M3", Developer: "MiniMax"},
	{ModelId: "TCADP/minimax-m3.1-flash", Name: "MiniMax-M3.1-Flash", Developer: "MiniMax"},
	{ModelId: "Deepseek/deepseek-v4.1-flash", Name: "DeepSeek-V4.1-Flash", Developer: "DeepSeek"},
	{ModelId: "Deepseek/deepseek-v4-pro-0813", Name: "DeepSeek-V4-Pro 0813 正式版", Developer: "DeepSeek"},
	{ModelId: "Deepseek/deepseek-v4-flash-202605", Name: "DeepSeek-V4.1-Flash-1017（原厂）", Developer: "DeepSeek"},
	{ModelId: "Deepseek/deepseek-v4-pro", Name: "DeepSeek-V4-Pro", Developer: "DeepSeek"},
	{ModelId: "Deepseek/deepseek-v4-pro-202606", Name: "DeepSeek-V4-Pro 0813（原厂）", Developer: "DeepSeek"},
	{ModelId: "Deepseek/deepseek-v4-flash-vision-exp", Name: "DeepSeek-V4-Flash-Vision-Exp（原厂）", Developer: "DeepSeek"},
	{ModelId: "Deepseek/deepseek-v4.1-flash-1017", Name: "DeepSeek-V4.1-Flash-1017", Developer: "DeepSeek"},
}

// ModelRegistry 模型白名单：启动时及每小时从 ADP DescribeModelList(ModelScene=18)
// 拉取 Claw 模式可用模型；拉取失败沿用上一次清单，从未成功则用内置静态清单。
type ModelRegistry struct {
	mu        sync.RWMutex
	models    []Model
	source    string // "adp" 或 "builtin"
	updatedAt time.Time
	lastErr   string
}

// NewModelRegistry 创建注册表，初始使用内置静态清单
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models:    builtinClawModels,
		source:    "builtin",
		updatedAt: time.Now(),
	}
}

// Refresh 从 ADP 拉取 Claw 可用模型（ModelScene=18），整页翻页聚合
func (r *ModelRegistry) Refresh(secretId, secretKey, spaceId, region string) (err error) {
	defer func() {
		if err != nil {
			r.mu.Lock()
			r.lastErr = err.Error()
			r.mu.Unlock()
		}
	}()
	const pageSize = 100
	type modelBasic struct {
		ModelId     string `json:"ModelId"`
		ModelName   string `json:"ModelName"`
		Name        string `json:"Name"`
		Description string `json:"Description"`
	}
	type developerInfo struct {
		Alias string `json:"Alias"`
		Name  string `json:"Name"`
	}
	type limitInfo struct {
		ContextLengthDescription string `json:"ContextLengthDescription"`
	}
	type modelItem struct {
		ModelBasic    modelBasic   `json:"ModelBasic"`
		DeveloperInfo developerInfo `json:"DeveloperInfo"`
		ProviderInfo  developerInfo `json:"ProviderInfo"`
		LimitInfo     limitInfo    `json:"LimitInfo"`
	}

	var allItems []modelItem
	page := 0
	for {
		params := map[string]interface{}{
			"SpaceId":    spaceId,
			"ModelScene": 18,
			"PageNumber": page,
			"PageSize":   pageSize,
		}
		var resp struct {
			Response struct {
				ModelList []modelItem `json:"ModelList"`
			} `json:"Response"`
		}
		if err := callADPAPI(secretId, secretKey, "DescribeModelList", region, params, &resp); err != nil {
			return err
		}
		items := resp.Response.ModelList
		allItems = append(allItems, items...)
		if len(items) < pageSize {
			break
		}
		page++
		if page > 10 { // 防御性上限
			break
		}
	}

	models := make([]Model, 0, len(allItems))
	seen := map[string]bool{}
	for _, it := range allItems {
		id := it.ModelBasic.ModelId
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		developer := it.DeveloperInfo.Alias
		if developer == "" {
			developer = it.DeveloperInfo.Name
		}
		provider := it.ProviderInfo.Alias
		if provider == "" {
			provider = it.ProviderInfo.Name
		}
		name := it.ModelBasic.Name
		if name == "" {
			name = id
		}
		models = append(models, Model{
			ModelId:    id,
			ModelName:  it.ModelBasic.ModelName,
			Name:       name,
			Developer:  developer,
			Provider:   provider,
			ContextLen: it.LimitInfo.ContextLengthDescription,
		})
	}
	if len(models) == 0 {
		return fmt.Errorf("ADP 返回模型清单为空")
	}

	r.mu.Lock()
	r.models = models
	r.source = "adp"
	r.updatedAt = time.Now()
	r.lastErr = ""
	r.mu.Unlock()
	log.Printf("[ModelRegistry] 模型白名单已刷新（来源=ADP，共 %d 个）", len(models))
	return nil
}

// List 返回当前清单副本
func (r *ModelRegistry) List() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, len(r.models))
	copy(out, r.models)
	return out
}

// Resolve 校验客户端传入的模型名：接受 ModelId / ModelName / Name，
// 大小写不敏感，ModelId 允许省略 "厂商/" 前缀（如 glm-5 命中 TCADP/glm-5）。
func (r *ModelRegistry) Resolve(name string) (Model, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Model{}, false
	}
	lower := strings.ToLower(name)
	bare := lower
	if idx := strings.LastIndex(lower, "/"); idx >= 0 {
		bare = lower[idx+1:]
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.models {
		mid := strings.ToLower(m.ModelId)
		mbare := mid
		if idx := strings.LastIndex(mid, "/"); idx >= 0 {
			mbare = mid[idx+1:]
		}
		if lower == mid || lower == mbare || bare == mbare ||
			lower == strings.ToLower(m.ModelName) || lower == strings.ToLower(m.Name) {
			return m, true
		}
	}
	return Model{}, false
}

// Source 返回当前清单来源（"adp" / "builtin"）
func (r *ModelRegistry) Source() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.source
}

// LastError 返回最近一次刷新失败的错误信息（空串表示无）
func (r *ModelRegistry) LastError() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastErr
}

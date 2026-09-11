//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// schedulingLinkageRepoStub 记录临时调度状态的写入/清除调用，
// 用于验证定时测试与错误策略/恢复链路的联动。
type schedulingLinkageRepoStub struct {
	mockAccountRepoForGemini

	account *Account

	getByIDCalls          int
	clearErrorCalls       int
	setModelRateLimitKeys []string
	setTempUnschedCalls   int
	setErrorCalls         int
	clearModelScopes      []string
	clearModelAllCalls    int
	clearTempUnschedCalls int
}

func (r *schedulingLinkageRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.getByIDCalls++
	return r.account, nil
}

func (r *schedulingLinkageRepoStub) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	r.setModelRateLimitKeys = append(r.setModelRateLimitKeys, scope)
	return nil
}

func (r *schedulingLinkageRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.setTempUnschedCalls++
	return nil
}

func (r *schedulingLinkageRepoStub) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	return nil
}

func (r *schedulingLinkageRepoStub) ClearModelRateLimit(ctx context.Context, id int64, scope string) error {
	r.clearModelScopes = append(r.clearModelScopes, scope)
	return nil
}

func (r *schedulingLinkageRepoStub) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.clearModelAllCalls++
	return nil
}

func (r *schedulingLinkageRepoStub) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempUnschedCalls++
	return nil
}

func (r *schedulingLinkageRepoStub) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	return nil
}

func (r *schedulingLinkageRepoStub) ClearRateLimit(ctx context.Context, id int64) error { return nil }

func (r *schedulingLinkageRepoStub) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	return nil
}

// kimiRuleAccount 构造一个启用了临时不可调度规则的 Kimi（OpenAI 兼容）账号。
// 注意：rules 必须是 []any（模拟 JSON 反序列化后的结构，与账号加载路径一致）。
func kimiRuleAccount(rules ...map[string]any) *Account {
	arr := make([]any, 0, len(rules))
	for _, r := range rules {
		arr = append(arr, r)
	}
	return &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Status:   StatusActive,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules":   arr,
		},
	}
}

func rule(errCode float64, keywords []any, minutes float64) map[string]any {
	return map[string]any{
		"error_code":       errCode,
		"keywords":         keywords,
		"duration_minutes": minutes,
	}
}

func newLinkageRunner(repo *schedulingLinkageRepoStub, cache *tempUnschedCacheRecorder) (*ScheduledTestRunnerService, *runtimeBlockRecorder) {
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
	blocker := &runtimeBlockRecorder{}
	svc.SetAccountRuntimeBlocker(blocker)
	return NewScheduledTestRunnerService(nil, nil, nil, svc, &config.Config{}, repo), blocker
}

func TestScheduledTestFailure_403MonthlyLimit_TriggersAccountTempUnschedulable(t *testing.T) {
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount(
		rule(403, []any{"You've reached your monthly usage limit for this billing cycle"}, 60),
	)}
	cache := &tempUnschedCacheRecorder{}
	runner, blocker := newLinkageRunner(repo, cache)

	result := &ScheduledTestResult{
		Status:       "failed",
		StatusCode:   http.StatusForbidden,
		ResponseBody: []byte(`{"error":{"message":"You've reached your monthly usage limit for this billing cycle"}}`),
	}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	// 定时测试命中规则 → 账号级临时不可调度（整账号停调）：
	// DB 写入 + Redis 缓存 + 调度阻断通知三件套齐全，而非只停被测模型；
	// 也不触发 SetError 等兜底。
	require.Equal(t, 1, repo.setTempUnschedCalls)
	require.Equal(t, []int64{42}, cache.setIDs)
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, int64(42), blocker.accounts[0].ID)
	require.Empty(t, repo.setModelRateLimitKeys)
	require.Zero(t, repo.setErrorCalls)
}

func TestScheduledTestFailure_429ResourceExhausted_TriggersAccountTempUnschedulable(t *testing.T) {
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount(
		rule(429, []any{"resource_exhausted"}, 5),
	)}
	cache := &tempUnschedCacheRecorder{}
	runner, blocker := newLinkageRunner(repo, cache)

	result := &ScheduledTestResult{
		Status:       "failed",
		StatusCode:   http.StatusTooManyRequests,
		ResponseBody: []byte(`{"error":{"code":"resource_exhausted"}}`),
	}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	require.Equal(t, 1, repo.setTempUnschedCalls)
	require.Equal(t, []int64{42}, cache.setIDs)
	require.Len(t, blocker.accounts, 1)
	require.Empty(t, repo.setModelRateLimitKeys)
	require.Zero(t, repo.setErrorCalls)
}

func TestScheduledTestFailure_Unknown403_DoesNotTriggerTempUnschedulable(t *testing.T) {
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount(
		rule(403, []any{"monthly usage limit"}, 60),
	)}
	cache := &tempUnschedCacheRecorder{}
	runner, blocker := newLinkageRunner(repo, cache)

	result := &ScheduledTestResult{
		Status:       "failed",
		StatusCode:   http.StatusForbidden,
		ResponseBody: []byte(`{"error":{"message":"unknown permission error"}}`),
	}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	// 未命中任何规则：只记录失败——不进入临时不可调度（三件套均无）、
	// 不写模型级 cooldown，也不触发兜底分支的 SetError 永久禁用。
	require.Empty(t, repo.setModelRateLimitKeys)
	require.Zero(t, repo.setTempUnschedCalls)
	require.Empty(t, cache.setIDs)
	require.Empty(t, blocker.accounts)
	require.Zero(t, repo.setErrorCalls)
}

func TestScheduledTestFailure_5HourAndWeeklyLimits_MatchConfiguredRules(t *testing.T) {
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount(
		rule(403, []any{"You've reached your 5-hour usage limit"}, 300),
		rule(403, []any{"You've reached your weekly (7-day) usage limit"}, 10080),
	)}
	runner, _ := newLinkageRunner(repo, &tempUnschedCacheRecorder{})

	for _, body := range []string{
		`{"error":{"message":"You've reached your 5-hour usage limit"}}`,
		`{"error":{"message":"You've reached your weekly (7-day) usage limit"}}`,
	} {
		result := &ScheduledTestResult{
			Status:       "failed",
			StatusCode:   http.StatusForbidden,
			ResponseBody: []byte(body),
		}
		runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)
	}

	require.Equal(t, 2, repo.setTempUnschedCalls)
	require.Empty(t, repo.setModelRateLimitKeys)
}

// mappedModelAccount 构造配置了模型映射的 Kimi 账号：别名 kimi-latest → 上游 moonshot-v1-kimi。
func mappedModelAccount(rules ...map[string]any) *Account {
	acc := kimiRuleAccount(rules...)
	acc.Credentials["model_mapping"] = map[string]any{
		"kimi-latest": "moonshot-v1-kimi",
	}
	return acc
}

func TestScheduledTestSuccess_ModelMapping_ClearsUpstreamModelKey(t *testing.T) {
	// 真实转发使用映射后的上游模型名作为 cooldown key，
	// 成功恢复回退计划侧别名时必须应用同一映射，否则 cooldown 永远清不掉。
	repo := &schedulingLinkageRepoStub{account: mappedModelAccount()}
	runner, _ := newLinkageRunner(repo, &tempUnschedCacheRecorder{})

	result := &ScheduledTestResult{Status: "success"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-latest", result)

	require.Equal(t, []string{"moonshot-v1-kimi"}, repo.clearModelScopes)
}

func TestScheduledTestFailure_401_SkipsRuleLinkage(t *testing.T) {
	// 401 在测试路径已有 SetError 完整处置；叠加规则联动会让 error 账号
	// 挂 temp-unsched 状态混杂，应跳过。
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount(
		rule(401, []any{"unauthorized"}, 10),
	)}
	runner, _ := newLinkageRunner(repo, &tempUnschedCacheRecorder{})

	result := &ScheduledTestResult{
		Status:       "failed",
		StatusCode:   http.StatusUnauthorized,
		ResponseBody: []byte(`{"error":{"message":"unauthorized"}}`),
	}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	require.Zero(t, repo.getByIDCalls)
	require.Empty(t, repo.setModelRateLimitKeys)
	require.Zero(t, repo.setTempUnschedCalls)
}

func TestScheduledTestFailure_WithoutHTTPContext_SkipsErrorPolicy(t *testing.T) {
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount(
		rule(403, []any{"monthly usage limit"}, 60),
	)}
	runner, _ := newLinkageRunner(repo, &tempUnschedCacheRecorder{})

	// 网络失败等场景：无结构化 HTTP 上下文，不猜测错误语义。
	result := &ScheduledTestResult{Status: "failed", ErrorMessage: "dial tcp: timeout"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	require.Zero(t, repo.getByIDCalls)
	require.Empty(t, repo.setModelRateLimitKeys)
	require.Zero(t, repo.setTempUnschedCalls)
}

func TestScheduledTestSuccess_ClearsOnlyTestedModelCooldown(t *testing.T) {
	repo := &schedulingLinkageRepoStub{account: kimiRuleAccount()}
	runner, _ := newLinkageRunner(repo, &tempUnschedCacheRecorder{})

	// 成功结果不携带结构化错误上下文（ModelID 仅在失败时透传），
	// 模型级恢复必须回退到计划侧配置的被测模型。
	result := &ScheduledTestResult{Status: "success"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	// 只清除被测模型的 cooldown：不得全量清除（kimi-k2 等仍有效的不受影响）。
	require.Equal(t, []string{"kimi-k3"}, repo.clearModelScopes)
	require.Zero(t, repo.clearModelAllCalls)
}

func TestScheduledTestSuccess_ClearsRuleTriggeredAccountTempUnschedulable(t *testing.T) {
	state := &TempUnschedState{
		UntilUnix:      time.Now().Add(time.Hour).Unix(),
		StatusCode:     http.StatusForbidden,
		RuleIndex:      0,
		MatchedKeyword: "monthly usage limit",
	}
	reason, err := json.Marshal(state)
	require.NoError(t, err)

	acc := kimiRuleAccount()
	acc.TempUnschedulableUntil = ptrTime(time.Now().Add(time.Hour))
	acc.TempUnschedulableReason = string(reason)

	repo := &schedulingLinkageRepoStub{account: acc}
	cache := &tempUnschedCacheRecorder{}
	runner, _ := newLinkageRunner(repo, cache)

	result := &ScheduledTestResult{Status: "success", ModelID: "kimi-k3"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Equal(t, []int64{42}, cache.deletedIDs)
}

func TestScheduledTestSuccess_ClearsAccountTempUnschedAndTestedModelKey_KeepsOtherModels(t *testing.T) {
	// 组合场景：失败联动写入账号级 temp-unsched 后，真实流量又对多个模型
	// 写入模型级 cooldown。被测模型成功 → 账号级 temp-unsched 与被测模型
	// key 都清除，其他模型的 cooldown 原样保留。
	state := &TempUnschedState{
		UntilUnix:  time.Now().Add(time.Hour).Unix(),
		StatusCode: http.StatusForbidden,
	}
	reason, err := json.Marshal(state)
	require.NoError(t, err)

	acc := kimiRuleAccount()
	acc.TempUnschedulableUntil = ptrTime(time.Now().Add(time.Hour))
	acc.TempUnschedulableReason = string(reason)
	acc.Extra = map[string]any{
		"model_rate_limits": map[string]any{
			"kimi-k2": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
			"kimi-k3": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
		},
	}

	repo := &schedulingLinkageRepoStub{account: acc}
	cache := &tempUnschedCacheRecorder{}
	runner, blocker := newLinkageRunner(repo, cache)

	result := &ScheduledTestResult{Status: "success"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	// 账号级：清除规则触发的 temp-unsched（DB + Redis + 解除通知）。
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Equal(t, []int64{42}, cache.deletedIDs)
	require.Equal(t, []int64{42}, blocker.clearedIDs)
	// 模型级：只清被测模型 key，kimi-k2 保留；不做全量清除。
	require.Equal(t, []string{"kimi-k3"}, repo.clearModelScopes)
	require.Zero(t, repo.clearModelAllCalls)
}

func TestScheduledTestSuccess_DoesNotClearNonRuleTempUnschedulable(t *testing.T) {
	// OAuth 401 冷却写入的 reason 是纯文本，不属于规则触发的临时不可调度，
	// 一次生成成功不得清除（可能只是 token 竞态窗口）。
	acc := kimiRuleAccount()
	acc.TempUnschedulableUntil = ptrTime(time.Now().Add(time.Hour))
	acc.TempUnschedulableReason = "Authentication failed (401): invalid or expired credentials"

	repo := &schedulingLinkageRepoStub{account: acc}
	cache := &tempUnschedCacheRecorder{}
	runner, _ := newLinkageRunner(repo, cache)

	result := &ScheduledTestResult{Status: "success", ModelID: "kimi-k3"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	require.Zero(t, repo.clearTempUnschedCalls)
	require.Empty(t, cache.deletedIDs)
	// 模型级恢复不受影响。
	require.Equal(t, []string{"kimi-k3"}, repo.clearModelScopes)
}

func TestScheduledTestSuccess_DoesNotTouchErrorStatus(t *testing.T) {
	// 手动禁用/永久错误不受成功恢复影响：恢复链路根本不调用 ClearError。
	acc := kimiRuleAccount()
	acc.Status = StatusError

	repo := &schedulingLinkageRepoStub{account: acc}
	runner, _ := newLinkageRunner(repo, &tempUnschedCacheRecorder{})

	result := &ScheduledTestResult{Status: "success", ModelID: "kimi-k3"}
	runner.applySchedulingSideEffects(context.Background(), 42, 1, "kimi-k3", result)

	require.Equal(t, []string{"kimi-k3"}, repo.clearModelScopes)
	// ClearError 属于 AutoRecover 门控的原有恢复路径，此处不应触碰。
	require.Zero(t, repo.clearErrorCalls)
}

//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 手动临时不可调度的桩：在 clear 桩基础上补充手动覆盖与自动清除的窄接口实现。
type manualTempUnschedRepoStub struct {
	rateLimitClearRepoStub

	overrideCalls int
	overrideUntil time.Time
	overrideArg   string
	overrideErr   error

	autoClearCalls int
	autoClearErr   error
}

func (r *manualTempUnschedRepoStub) SetTempUnschedulableOverride(ctx context.Context, id int64, until time.Time, reason string) error {
	r.overrideCalls++
	r.overrideUntil = until
	r.overrideArg = reason
	return r.overrideErr
}

func (r *manualTempUnschedRepoStub) ClearTempUnschedulableAuto(ctx context.Context, id int64) (bool, error) {
	r.autoClearCalls++
	return true, r.autoClearErr
}

func TestRateLimitService_SetManualTempUnschedulable_WritesManualPrefixAndReturnsAccount(t *testing.T) {
	account := &Account{ID: 42, Status: StatusActive}
	repo := &manualTempUnschedRepoStub{}
	repo.getByIDAccount = account
	cache := &tempUnschedCacheRecorder{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)

	before := time.Now()
	updated, err := svc.SetManualTempUnschedulable(context.Background(), 42, 30, "维护窗口")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, int64(42), updated.ID)

	require.Equal(t, 1, repo.overrideCalls)
	require.Equal(t, "manual:维护窗口", repo.overrideArg)
	// until ≈ now + 30 分钟
	require.WithinDuration(t, before.Add(30*time.Minute), repo.overrideUntil, 2*time.Minute)
	// 写入后使缓存中的旧自动状态失效
	require.Equal(t, []int64{42}, cache.deletedIDs)
}

func TestRateLimitService_SetManualTempUnschedulable_RejectsInvalidDuration(t *testing.T) {
	repo := &manualTempUnschedRepoStub{getByIDAccount: &Account{ID: 42}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	for _, minutes := range []int{0, -1, 10081} {
		updated, err := svc.SetManualTempUnschedulable(context.Background(), 42, minutes, "")
		require.Error(t, err, "duration %d must be rejected", minutes)
		require.Nil(t, updated)
	}
	require.Equal(t, 0, repo.overrideCalls, "invalid duration must not reach the repository")
}

func TestRateLimitService_SetManualTempUnschedulable_DefaultReasonAndTruncation(t *testing.T) {
	repo := &manualTempUnschedRepoStub{getByIDAccount: &Account{ID: 42}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	// 未填写原因时使用默认文案
	_, err := svc.SetManualTempUnschedulable(context.Background(), 42, 15, "  ")
	require.NoError(t, err)
	require.Equal(t, "manual:管理员手动设置", repo.overrideArg)

	// 超过 200 字符的原因截断到 200
	long := strings.Repeat("长", 250)
	_, err = svc.SetManualTempUnschedulable(context.Background(), 42, 15, long)
	require.NoError(t, err)
	require.Equal(t, "manual:"+strings.Repeat("长", 200), repo.overrideArg)
}

func TestRateLimitService_SetManualTempUnschedulable_RepoWithoutOverrideCapabilityFails(t *testing.T) {
	// 不实现 TempUnschedOverrideRepo 的桩：装配缺陷必须大声报错而不是静默降级。
	repo := &rateLimitClearRepoStub{getByIDAccount: &Account{ID: 42}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	updated, err := svc.SetManualTempUnschedulable(context.Background(), 42, 15, "")
	require.Error(t, err)
	require.Nil(t, updated)
}

// 显式前缀守卫：即使手动 reason 恰好是合法的规则 JSON，也不能被自动恢复路径识别为规则触发。
func TestIsRuleTriggeredTempUnsched_ExcludesManualPrefixExplicitly(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	manualJSONShaped := ManualTempUnschedReasonPrefix + `{"status_code":429,"until_unix":1}`

	require.False(t, isRuleTriggeredTempUnsched(&Account{
		TempUnschedulableUntil:  &future,
		TempUnschedulableReason: manualJSONShaped,
	}), "manual mark with JSON-shaped reason must be explicitly excluded")

	require.False(t, isRuleTriggeredTempUnsched(&Account{
		TempUnschedulableUntil:  &future,
		TempUnschedulableReason: "manual:管理员手动设置",
	}), "plain manual mark must be excluded")

	require.True(t, isRuleTriggeredTempUnsched(&Account{
		TempUnschedulableUntil:  &future,
		TempUnschedulableReason: `{"status_code":429,"until_unix":1}`,
	}), "rule-triggered JSON reason must still be recognized")
}

func TestRateLimitService_ClearRateLimit_AutoPathPrefersManualExemptClear(t *testing.T) {
	repo := &manualTempUnschedRepoStub{}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)

	// 自动恢复路径：使用 ClearTempUnschedulableAuto（跳过 manual 标记）
	err := svc.clearRateLimit(context.Background(), 42, true)
	require.NoError(t, err)
	require.Equal(t, 1, repo.autoClearCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)

	// 管理端路径：使用普通 ClearTempUnschedulable（可清除 manual 标记）
	err = svc.ClearRateLimit(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
}

func TestRateLimitService_ClearRateLimit_AutoPathFallsBackWhenAutoClearUnavailable(t *testing.T) {
	// 仅测试桩场景：仓储未实现自动清除时回退到普通清除，真实仓储始终实现两者。
	repo := &rateLimitClearRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	err := svc.clearRateLimit(context.Background(), 42, true)
	require.NoError(t, err)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
}

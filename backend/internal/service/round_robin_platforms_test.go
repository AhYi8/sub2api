//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件覆盖 Claude 通用链路与 Gemini Messages 兼容链路的严格轮询注入。
// 依赖 //go:build unit 标签下的链路级 mock（mockAccountRepoForPlatform 等），
// 因此与 openai_account_scheduler_round_robin_test.go（无标签）拆分为两个文件。

// TestGatewayService_RoundRobin_LoadAwareLayer2Rotates 验证：
// Claude 通用链路（负载感知管线 Layer 2）轮询模式下连续调度轮过全部候选
// （按 ID 升序），且不写 session 粘性绑定。
func TestGatewayService_RoundRobin_LoadAwareLayer2Rotates(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30101)
	requestedModel := "claude-sonnet-4-5"

	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 37001, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true},
			{ID: 37002, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true},
			{ID: 37003, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForPlatform{}
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:       groupID,
				Name:     "rr-anthropic-group",
				Platform: PlatformAnthropic,
				Status:   StatusActive,
				Hydrated: true,
			},
		},
	}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	settingSvc := NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	}}, testConfig())
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true

	svc := &GatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                cfg,
		groupRepo:          groupRepo,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		settingService:     settingSvc,
	}

	got := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", requestedModel, nil, "", 0)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, result.Account)
		got = append(got, result.Account.ID)
	}
	// 三次调度依次轮过全部候选，而不是按 LRU 固定复用同一账号
	require.Equal(t, []int64{37001, 37002, 37003}, got)
	// 轮询期间不写粘性绑定
	require.Empty(t, cache.sessionBindings)
}

// TestGeminiMessagesCompatService_RoundRobin_RotatesAndKeepsBinding 验证：
// Gemini Messages 兼容链路轮询模式下连续调度轮过全部候选，
// 且既有粘性绑定不生效也不被删除/覆盖。
func TestGeminiMessagesCompatService_RoundRobin_RotatesAndKeepsBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30201)

	accounts := []Account{
		{ID: 38001, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1},
		{ID: 38002, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1},
		{ID: 38003, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 1, Concurrency: 1},
	}
	repo := &mockAccountRepoForPlatform{
		accounts:     accounts,
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForPlatform{}
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {
				ID:       groupID,
				Name:     "rr-gemini-group",
				Platform: PlatformGemini,
				Status:   StatusActive,
				Hydrated: true,
			},
		},
	}
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	settingSvc := NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	}}, testConfig())

	svc := NewGeminiMessagesCompatService(
		repo,
		groupRepo,
		cache,
		nil,
		nil,
		&RateLimitService{settingService: settingSvc},
		nil,
		nil,
		testConfig(),
	)
	// 预置粘性绑定：该会话原本固定在 38002
	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, "gemini:sess-rr", 38002, time.Hour))

	got := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		acc, err := svc.SelectAccountForModel(ctx, &groupID, "sess-rr", "")
		require.NoError(t, err)
		require.NotNil(t, acc)
		got = append(got, acc.ID)
	}
	// 粘性账号不再垄断：三次调度覆盖全部三个候选
	require.Equal(t, []int64{38001, 38002, 38003}, got)
	// 原绑定原样保留，切回默认策略后立即可恢复粘性
	require.Equal(t, int64(38002), cache.sessionBindings["gemini:sess-rr"])
}

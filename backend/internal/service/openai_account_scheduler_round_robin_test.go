package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newRoundRobinTestOpenAIService 构造带「严格轮询」系统设置的 OpenAI 网关服务。
// gatewayForwardingCache 为包级缓存，先写入过期缓存强制本次从 repo 读取，
// 避免其他用例遗留的 60s 缓存污染测试结果。
func newRoundRobinTestOpenAIService(
	t *testing.T,
	cfg *config.Config,
	accounts []Account,
	repoValues map[string]string,
) (*OpenAIGatewayService, *schedulerTestGatewayCache) {
	t.Helper()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	settingSvc := NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: repoValues}, cfg)
	cache := &schedulerTestGatewayCache{}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		settingService:     settingSvc,
		// 高级调度器设置经 rateLimitService.settingService.settingRepo 读取
		rateLimitService: &RateLimitService{settingService: settingSvc},
	}
	return svc, cache
}

func roundRobinTestAccounts(groupID int64, ids ...int64) []Account {
	accounts := make([]Account, 0, len(ids))
	for _, id := range ids {
		accounts = append(accounts, Account{
			ID:          id,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			// 粘性层的分组匹配 gate 依赖分组元数据：groupID 非 nil 时，
			// 无归属信息的账号会被判为不属于该分组（与生产数据一致，补全归属）。
			AccountGroups: []AccountGroup{{GroupID: groupID}},
		})
	}
	return accounts
}

// TestOpenAIGatewayService_RoundRobin_LoadBalanceRotates 验证：
// 高级调度器关闭（默认形态）+ 严格轮询策略时，同一分组连续调度依次轮过全部
// 可用账号（按 ID 升序），且不写 session 粘性绑定。
func TestOpenAIGatewayService_RoundRobin_LoadBalanceRotates(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(20101)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc, cache := newRoundRobinTestOpenAIService(t, cfg, roundRobinTestAccounts(groupID, 36003, 36002, 36001), map[string]string{
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	})
	ctx := context.Background()

	// 三次调度应依次命中 ID 升序候选（游标 1/2/3 → 起始位 0/1/2）
	got := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		selection, decision, err := svc.SelectAccountWithScheduler(
			ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		got = append(got, selection.Account.ID)
	}
	require.Equal(t, []int64{36001, 36002, 36003}, got)

	// 轮询期间不得写入粘性绑定，切回默认策略后不受残留影响
	require.Empty(t, cache.sessionBindings)
}

// TestOpenAIGatewayService_RoundRobin_IgnoresStickyBinding 验证：
// 严格轮询下既有粘性绑定既不生效也不被删除/覆盖。
func TestOpenAIGatewayService_RoundRobin_IgnoresStickyBinding(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(20102)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc, cache := newRoundRobinTestOpenAIService(t, cfg, roundRobinTestAccounts(groupID, 36001, 36002, 36003), map[string]string{
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	})
	ctx := context.Background()
	// 预置粘性绑定：该会话原本固定在 36002（经服务封装写入，key 带 openai: 前缀）
	require.NoError(t, svc.setStickySessionAccountID(ctx, &groupID, "sess-rr", 36002, time.Hour))

	got := make(map[int64]bool, 3)
	for i := 0; i < 3; i++ {
		selection, _, err := svc.SelectAccountWithScheduler(
			ctx, &groupID, "", "sess-rr", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		got[selection.Account.ID] = true
	}
	// 粘性账号不再垄断：三次调度覆盖全部三个候选
	require.Len(t, got, 3)
	// 原绑定原样保留，切回默认策略后立即可恢复粘性
	require.Equal(t, int64(36002), cache.sessionBindings["openai:sess-rr"])
}

// TestOpenAIGatewayService_RoundRobin_AdvancedSchedulerSelectionOrderRotates 验证：
// 高级调度器开启时，轮询策略跳过 top-K 加权随机，按 ID 升序轮询。
func TestOpenAIGatewayService_RoundRobin_AdvancedSchedulerSelectionOrderRotates(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(20103)
	cfg := newSchedulerTestSubscriptionPriorityConfig()
	svc, _ := newRoundRobinTestOpenAIService(t, cfg, roundRobinTestAccounts(groupID, 36002, 36001), map[string]string{
		openAIAdvancedSchedulerSettingKey:   "true",
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	})
	ctx := context.Background()

	require.True(t, svc.isOpenAIAdvancedSchedulerEnabled(ctx))

	first, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, int64(36001), first.Account.ID)

	second, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, int64(36002), second.Account.ID)
}

// TestOpenAIGatewayService_RoundRobin_PreviousResponseStillSticky 验证：
// 严格轮询不影响 previous_response_id 硬粘层（上游会话状态绑定账号，必须保留）。
func TestOpenAIGatewayService_RoundRobin_PreviousResponseStillSticky(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(20104)
	cfg := newSchedulerTestOpenAIWSV2Config()
	svc, _ := newRoundRobinTestOpenAIService(t, cfg, roundRobinTestAccounts(groupID, 36001, 36002), map[string]string{
		openAIAdvancedSchedulerSettingKey:   "true",
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	})
	ctx := context.Background()

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_rr_sticky", 36002, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "resp_rr_sticky", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.Equal(t, int64(36002), selection.Account.ID)
}

// TestOpenAIGatewayService_RoundRobin_DefaultStrategyKeepsSticky 验证：
// 默认策略（未开启轮询）下粘性命中行为不变，作为轮询改造的回归保护。
func TestOpenAIGatewayService_RoundRobin_DefaultStrategyKeepsSticky(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(20105)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc, _ := newRoundRobinTestOpenAIService(t, cfg, roundRobinTestAccounts(groupID, 36001, 36002), map[string]string{
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyDefault,
	})
	ctx := context.Background()
	// 经服务封装写入粘性绑定（key 带 openai: 前缀）
	require.NoError(t, svc.setStickySessionAccountID(ctx, &groupID, "sess-default", 36002, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "sess-default", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(36002), selection.Account.ID)
	require.False(t, decision.StickySessionHit)
}

// TestOpenAIGatewayService_RoundRobin_GuardianParentStillSticky 验证：
// 严格轮询不影响 guardian_parent 守护绑定层——Codex review 等子代理请求必须
// 固定到父线程账号（上游会话状态绑定账号，轮询不得打破）。
func TestOpenAIGatewayService_RoundRobin_GuardianParentStillSticky(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	parentID := "33333333-3333-4333-8333-333333333333"
	parentHash := DeriveSessionHashFromSeed(parentID)
	groupID := int64(20106)
	accounts := []Account{
		{
			ID: 39001, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10,
			GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"},
		},
		{
			ID: 39002, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"},
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	settingSvc := NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		openAIAdvancedSchedulerSettingKey:   "true",
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyRoundRobin,
	}}, cfg)
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + parentHash: 39001}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              cache,
		cfg:                cfg,
		settingService:     settingSvc,
		rateLimitService:   &RateLimitService{settingService: settingSvc},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{39001: true, 39002: true}}),
	}

	ctx := guardianAffinityTestContext(t, codexAutoReviewModel, "guardian", parentID, "")
	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "guardian-child-session", codexAutoReviewModel,
		nil, OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	// 轮询开启时守护绑定层仍命中父线程账号
	require.Equal(t, openAIAccountScheduleLayerGuardianParent, decision.Layer)
	require.Equal(t, int64(39001), selection.Account.ID)
	// 守护绑定不被删除
	require.Zero(t, cache.deletedSessions["openai:"+parentHash])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

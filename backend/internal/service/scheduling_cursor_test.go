package service

import (
	"context"
	"errors"
	"testing"
)

type stubRoundRobinCursorStore struct {
	value int64
	err   error
	calls int
}

func (s *stubRoundRobinCursorStore) NextRoundRobinCursor(ctx context.Context, scope string) (int64, error) {
	s.calls++
	return s.value, s.err
}

func TestRotateStartIndex(t *testing.T) {
	cases := []struct {
		name   string
		cursor int64
		n      int
		want   int
	}{
		{"空候选池", 1, 0, 0},
		{"单账号恒为 0", 42, 1, 0},
		{"首游标映射 0", 1, 3, 0},
		{"第二游标映射 1", 2, 3, 1},
		{"游标越过一圈回绕", 4, 3, 0},
		{"非法游标按 1 处理", 0, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rotateStartIndex(tc.cursor, tc.n); got != tc.want {
				t.Fatalf("rotateStartIndex(%d, %d) = %d, want %d", tc.cursor, tc.n, got, tc.want)
			}
		})
	}
}

// TestRotateStartIndexStrictRotation 验证严格轮询核心语义：
// 候选数 N>=2 时，连续递增的游标值映射到互不相同的起始下标（绕圈周期为 N）。
func TestRotateStartIndexStrictRotation(t *testing.T) {
	const n = 4
	seen := make(map[int]bool, n)
	for cursor := int64(1); cursor <= n; cursor++ {
		start := rotateStartIndex(cursor, n)
		if seen[start] {
			t.Fatalf("cursor=%d 起始下标 %d 重复，破坏严格轮询语义", cursor, start)
		}
		seen[start] = true
	}
}

func TestRoundRobinScope(t *testing.T) {
	groupID := int64(7)
	if got := roundRobinScope(&groupID, "openai"); got != "7:openai" {
		t.Fatalf("roundRobinScope = %q, want %q", got, "7:openai")
	}
	// 无分组（nil groupID）也必须生成稳定且不与分组作用域冲突的 scope
	if got := roundRobinScope(nil, "openai"); got != "0:openai" {
		t.Fatalf("roundRobinScope(nil) = %q, want %q", got, "0:openai")
	}
}

func TestRoundRobinCursorManagerNextLocal(t *testing.T) {
	manager := newRoundRobinCursorManager(nil)
	ctx := context.Background()

	first := manager.next(ctx, "1:openai")
	second := manager.next(ctx, "1:openai")
	if first != 1 || second != 2 {
		t.Fatalf("同 scope 游标应连续递增，got %d, %d", first, second)
	}

	other := manager.next(ctx, "1:anthropic")
	if other != 1 {
		t.Fatalf("不同 scope 游标应独立，got %d, want 1", other)
	}
}

func TestRoundRobinCursorManagerRedisFailureFallsBackLocal(t *testing.T) {
	store := &stubRoundRobinCursorStore{err: errors.New("redis down")}
	manager := newRoundRobinCursorManager(store)
	ctx := context.Background()

	if got := manager.next(ctx, "3:openai"); got != 1 {
		t.Fatalf("Redis 失败应降级进程内游标，got %d, want 1", got)
	}
	if store.calls != 1 {
		t.Fatalf("应尝试一次 Redis 调用，got %d", store.calls)
	}
}

func TestRoundRobinCursorManagerRedisValueWins(t *testing.T) {
	store := &stubRoundRobinCursorStore{value: 9}
	manager := newRoundRobinCursorManager(store)
	ctx := context.Background()

	if got := manager.next(ctx, "3:openai"); got != 9 {
		t.Fatalf("应使用 Redis 返回值，got %d, want 9", got)
	}
	if manager.nextLocal("3:openai") != 1 {
		t.Fatalf("Redis 成功时不应推进进程内降级游标")
	}
}

// TestSettingService_GetAccountSchedulingStrategyForPlatform 验证两级策略解析：
// 平台级覆盖（default/round_robin）优先；system 与缺省一律继承系统级；
// 未知平台键与非法值在解析层已被剔除，不会进入运行时。
func TestSettingService_GetAccountSchedulingStrategyForPlatform(t *testing.T) {
	ctx := context.Background()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	svc := NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		SettingKeyAccountSchedulingStrategy:           AccountSchedulingStrategyRoundRobin,
		SettingKeyAccountSchedulingStrategyByPlatform: `{"openai":"default","grok":"round_robin","anthropic":"system","mystery":"round_robin"}`,
	}}, nil)

	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, PlatformOpenAI); got != AccountSchedulingStrategyDefault {
		t.Fatalf("openai 平台覆盖 default 生效，got %q", got)
	}
	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, PlatformGrok); got != AccountSchedulingStrategyRoundRobin {
		t.Fatalf("grok 平台覆盖 round_robin 生效，got %q", got)
	}
	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, PlatformAnthropic); got != AccountSchedulingStrategyRoundRobin {
		t.Fatalf("anthropic 显式 system 应继承系统级 round_robin，got %q", got)
	}
	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, PlatformGemini); got != AccountSchedulingStrategyRoundRobin {
		t.Fatalf("gemini 未配置应继承系统级 round_robin，got %q", got)
	}
	// kimi/zhipu/deepseek 走 OpenAI 兼容链调度，同样在白名单内可覆盖/继承
	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, PlatformKimi); got != AccountSchedulingStrategyRoundRobin {
		t.Fatalf("kimi 未配置应继承系统级 round_robin，got %q", got)
	}
	// antigravity（/antigravity 强制平台路由）同样可继承
	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, PlatformAntigravity); got != AccountSchedulingStrategyRoundRobin {
		t.Fatalf("antigravity 未配置应继承系统级 round_robin，got %q", got)
	}
	// 未知平台键被剔除，同样走继承
	if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, "mystery"); got != AccountSchedulingStrategyRoundRobin {
		t.Fatalf("未知平台键应被剔除并继承系统级，got %q", got)
	}
}

// TestSettingService_GetAccountSchedulingStrategyForPlatform_InheritsDefaultSystem
// 系统级为 default 时，任何平台级配置缺省/继承都解析为 default。
func TestSettingService_GetAccountSchedulingStrategyForPlatform_InheritsDefaultSystem(t *testing.T) {
	ctx := context.Background()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{expiresAt: 0})
	svc := NewSettingService(&openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		SettingKeyAccountSchedulingStrategy: AccountSchedulingStrategyDefault,
	}}, nil)

	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini} {
		if got := svc.GetAccountSchedulingStrategyForPlatform(ctx, platform); got != AccountSchedulingStrategyDefault {
			t.Fatalf("平台 %s 未配置应继承系统级 default，got %q", platform, got)
		}
	}
}

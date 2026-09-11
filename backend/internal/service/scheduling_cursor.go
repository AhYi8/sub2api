package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SchedulingCursorStore 提供「严格轮询」调度策略所需的原子递增游标。
// 生产实现见 repository.gatewayCache（Lua 脚本 INCR+PEXPIRE 原子完成，多实例共享，
// 保证严格轮询语义跨实例一致）；单测 stub 未实现该接口时走进程内降级。
type SchedulingCursorStore interface {
	// NextRoundRobinCursor 原子递增指定作用域的轮询游标并返回新值（从 1 开始）。
	NextRoundRobinCursor(ctx context.Context, scope string) (int64, error)
}

// SchedulingRoundRobinCursorTTL 游标滑动 TTL：长期无请求的分组自动清理，
// 避免游标 key 无限累积；有请求时每次访问都续期。
// repository 侧的 Lua 脚本以毫秒形式引用同一常量，勿在别处重复硬编码。
const SchedulingRoundRobinCursorTTL = 7 * 24 * time.Hour

// schedulingRoundRobinCursorRedisTimeout 轮询在请求热路径上执行，Redis 异常时
// 不能拖慢调度：超过该时长未返回即降级进程内游标。
const schedulingRoundRobinCursorRedisTimeout = 200 * time.Millisecond

// roundRobinCursorManager 统一封装轮询游标的获取与降级：
// 优先 Redis（多实例严格一致），失败时降级为进程内计数器（退化为实例内轮询），
// 永不向调度热路径返回错误。
type roundRobinCursorManager struct {
	store SchedulingCursorStore // 可为 nil（无 Redis 实现）
	local sync.Map              // scope -> *atomic.Int64（降级用）
}

func newRoundRobinCursorManager(store SchedulingCursorStore) *roundRobinCursorManager {
	return &roundRobinCursorManager{store: store}
}

// next 返回指定作用域的下一次轮询游标值（从 1 开始单调递增）。
// scope 由调用方按 (groupID, platform) 构造，保证不同候选池互不干扰。
func (m *roundRobinCursorManager) next(ctx context.Context, scope string) int64 {
	if m == nil {
		return 1
	}
	if m.store != nil {
		// 热路径防阻塞：Redis 卡顿时快速降级，不让一次调度无限等待。
		redisCtx, cancel := context.WithTimeout(ctx, schedulingRoundRobinCursorRedisTimeout)
		value, err := m.store.NextRoundRobinCursor(redisCtx, scope)
		cancel()
		if err == nil && value > 0 {
			return value
		}
	}
	return m.nextLocal(scope)
}

// nextLocal 进程内降级游标：Redis 不可用时保证调度仍可进行，
// 语义从「多实例严格轮询」退化为「单实例严格轮询」。
func (m *roundRobinCursorManager) nextLocal(scope string) int64 {
	var cursor *atomic.Int64
	if loaded, ok := m.local.Load(scope); ok {
		cursor, _ = loaded.(*atomic.Int64)
	} else {
		cursor = &atomic.Int64{}
		loaded, _ = m.local.LoadOrStore(scope, cursor)
		// sync.Map 的值仅由本方法写入且恒为 *atomic.Int64，断言 ok 恒为 true；
		// 双值形式仅为满足 errcheck 的类型断言检查（check-type-assertions）。
		cursor, _ = loaded.(*atomic.Int64)
	}
	return cursor.Add(1)
}

// roundRobinScope 构造轮询游标作用域：按 (groupID, platform) 隔离，
// 不同分组/平台的候选池互不推进对方的游标。
func roundRobinScope(groupID *int64, platform string) string {
	return fmt.Sprintf("%d:%s", derefGroupID(groupID), platform)
}

// rotateStartIndex 由游标值计算轮询起始下标：候选必须已按稳定顺序（ID 升序）排列，
// 否则取模结果会随排序抖动，破坏「连续请求不重号」的严格性。
// cursor 从 1 开始，映射到 [0, n)。
func rotateStartIndex(cursor int64, n int) int {
	if n <= 0 {
		return 0
	}
	if cursor < 1 {
		cursor = 1
	}
	return int((cursor - 1) % int64(n))
}

// schedulingCursorStoreFor 从已注入的 GatewayCache 中探测游标能力：
// 生产实现（repository.gatewayCache）同时实现 SchedulingCursorStore，
// 测试 stub 未实现时返回 nil，自动走进程内降级。
func schedulingCursorStoreFor(cache GatewayCache) SchedulingCursorStore {
	if store, ok := cache.(SchedulingCursorStore); ok {
		return store
	}
	return nil
}

// roundRobinManager 返回服务级游标管理器（懒初始化，atomic.Pointer 保证
// 并发首次调度下无数据竞争；重复构建的实例经 CompareAndSwap 只保留一个）。
func roundRobinManager(
	field *atomic.Pointer[roundRobinCursorManager],
	cache GatewayCache,
) *roundRobinCursorManager {
	if m := field.Load(); m != nil {
		return m
	}
	m := newRoundRobinCursorManager(schedulingCursorStoreFor(cache))
	if field.CompareAndSwap(nil, m) {
		return m
	}
	return field.Load()
}

// GatewayService 侧封装：判断当前是否启用严格轮询策略。
// settingService 不可用时始终视为默认策略（安全侧：保持既有行为）。
func (s *GatewayService) accountSchedulingRoundRobinEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	return s.settingService.GetAccountSchedulingStrategy(ctx) == AccountSchedulingStrategyRoundRobin
}

// nextRoundRobinCursor 取一次轮询游标原始值。
// 注意：一次调度只应调用一次；同一调度内多个候选池需复用同一游标值
// （各池按自身大小取模），否则会破坏「每次调度推进一次游标」的严格性。
func (s *GatewayService) nextRoundRobinCursor(ctx context.Context, groupID *int64, platform string) int64 {
	return roundRobinManager(&s.roundRobinCursors, s.cache).next(ctx, roundRobinScope(groupID, platform))
}

// OpenAIGatewayService 侧封装（语义与 GatewayService 侧一致）。

func (s *OpenAIGatewayService) accountSchedulingRoundRobinEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	return s.settingService.GetAccountSchedulingStrategy(ctx) == AccountSchedulingStrategyRoundRobin
}

func (s *OpenAIGatewayService) nextRoundRobinCursor(ctx context.Context, groupID *int64, platform string) int64 {
	return roundRobinManager(&s.roundRobinCursors, s.cache).next(ctx, roundRobinScope(groupID, platform))
}

func (s *OpenAIGatewayService) nextRoundRobinStart(ctx context.Context, groupID *int64, platform string, candidateCount int) int {
	if candidateCount <= 1 {
		return 0
	}
	return rotateStartIndex(s.nextRoundRobinCursor(ctx, groupID, platform), candidateCount)
}

// GeminiMessagesCompatService 侧封装（语义与 GatewayService 侧一致；
// 该服务无直接 SettingService 引用，经 rateLimitService.settingService 访问）。

func (s *GeminiMessagesCompatService) accountSchedulingRoundRobinEnabled(ctx context.Context) bool {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.settingService == nil {
		return false
	}
	return s.rateLimitService.settingService.GetAccountSchedulingStrategy(ctx) == AccountSchedulingStrategyRoundRobin
}

func (s *GeminiMessagesCompatService) nextRoundRobinCursor(ctx context.Context, groupID *int64, platform string) int64 {
	return roundRobinManager(&s.roundRobinCursors, s.cache).next(ctx, roundRobinScope(groupID, platform))
}

func (s *GeminiMessagesCompatService) nextRoundRobinStart(ctx context.Context, groupID *int64, platform string, candidateCount int) int {
	if candidateCount <= 1 {
		return 0
	}
	return rotateStartIndex(s.nextRoundRobinCursor(ctx, groupID, platform), candidateCount)
}

// rotateOpenAICandidatesWithStart 将 OpenAI 候选池按 ID 升序稳定排序后，
// 从 roundRobinStart(poolSize) 计算的游标位旋转，作为完整 selectionOrder
// （覆盖 top-K 与加权随机的轮询替代）。start 回调由调用方传入，同一调度内
// 的多个候选池（compact 分层）共用同一次游标取值。
func rotateOpenAICandidatesWithStart(
	req OpenAIAccountScheduleRequest,
	pool []openAIAccountCandidateScore,
	roundRobinStart func(poolSize int) int,
) []openAIAccountCandidateScore {
	if len(pool) <= 1 {
		return append([]openAIAccountCandidateScore(nil), pool...)
	}
	ordered := append([]openAIAccountCandidateScore(nil), pool...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].account.ID < ordered[j].account.ID
	})
	start := roundRobinStart(len(ordered))
	return append(ordered[start:], ordered[:start]...)
}

// rotateOpenAIAccountsRoundRobin 旧链路的账号级轮询旋转：
// 非 compact 请求全池按 ID 升序 + 游标旋转；compact 请求保持支持层级顺序（2→1），
// 且只对「本次真正产出首选」的层级推进一次游标，另一层级仅按层内 ID 升序追加，
// 避免被丢弃的旋转结果空转消耗游标（破坏轮换相位）。
func (s *OpenAIGatewayService) rotateOpenAIAccountsRoundRobin(
	ctx context.Context,
	groupID *int64,
	platform string,
	eligible []*Account,
	compactTiers map[int64]int,
	requireCompact bool,
) []*Account {
	rotate := func(pool []*Account) []*Account {
		if len(pool) <= 1 {
			return pool
		}
		sort.SliceStable(pool, func(i, j int) bool {
			return pool[i].ID < pool[j].ID
		})
		start := s.nextRoundRobinStart(ctx, groupID, platform, len(pool))
		return append(pool[start:], pool[:start]...)
	}
	sortOnly := func(pool []*Account) []*Account {
		sort.SliceStable(pool, func(i, j int) bool {
			return pool[i].ID < pool[j].ID
		})
		return pool
	}
	if !requireCompact {
		return rotate(eligible)
	}
	tier2 := make([]*Account, 0, len(eligible))
	tier1 := make([]*Account, 0, len(eligible))
	for _, acc := range eligible {
		if compactTiers[acc.ID] == 2 {
			tier2 = append(tier2, acc)
		} else {
			tier1 = append(tier1, acc)
		}
	}
	if len(tier2) > 0 {
		return append(rotate(tier2), sortOnly(tier1)...)
	}
	return rotate(tier1)
}

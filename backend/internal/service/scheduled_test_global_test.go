package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubGlobalPlanRepo 最小实现计划仓储接口，用于全局定时测试的 service 层单测。
type stubGlobalPlanRepo struct {
	ScheduledTestPlanRepository
	global  *ScheduledTestPlan
	byID    map[int64]*ScheduledTestPlan
	deleted []int64
	updated *ScheduledTestPlan
}

func (s *stubGlobalPlanRepo) GetGlobal(ctx context.Context) (*ScheduledTestPlan, error) {
	if s.global == nil {
		return nil, errors.New("not found")
	}
	return s.global, nil
}

func (s *stubGlobalPlanRepo) GetByID(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	if p, ok := s.byID[id]; ok {
		return p, nil
	}
	return nil, errors.New("not found")
}

func (s *stubGlobalPlanRepo) Delete(ctx context.Context, id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *stubGlobalPlanRepo) Update(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	s.updated = plan
	return plan, nil
}

// claimRecorder 记录 ClaimForRun 调用，用于断言批次互斥行为。
type claimRecorder struct {
	stubGlobalPlanRepo
	claims []int64
}

func (s *claimRecorder) ClaimForRun(ctx context.Context, id int64, now time.Time, nextRunAt time.Time) (bool, error) {
	s.claims = append(s.claims, id)
	return true, nil
}

// TestRunGlobalPlan_SkipsWhilePreviousBatchRunning 验证进程内互斥：
// 上一批次未完成（globalRunning=true）时，新一轮到期直接跳过，不发起认领。
func TestRunGlobalPlan_SkipsWhilePreviousBatchRunning(t *testing.T) {
	repo := &claimRecorder{}
	runner := NewScheduledTestRunnerService(repo, nil, nil, nil, nil, nil)
	plan := &ScheduledTestPlan{ID: 1, CronExpression: "0 * * * *", PlatformModels: map[string]string{"anthropic": "m"}}

	// 模拟上一批次仍在执行
	runner.globalRunning.Store(true)
	runner.runGlobalPlan(plan)

	if len(repo.claims) != 0 {
		t.Fatalf("expected no claim while previous batch is running, got %d claims", len(repo.claims))
	}

	// 上一批次结束后（标志复位），下一轮可正常认领
	runner.globalRunning.Store(false)
	runner.runGlobalPlan(plan)
	if len(repo.claims) != 1 {
		t.Fatalf("expected exactly one claim after previous batch finished, got %d", len(repo.claims))
	}
	if runner.globalRunning.Load() {
		t.Fatalf("expected globalRunning to be released after runGlobalPlan returns")
	}
}

type stubGlobalResultRepo struct {
	ScheduledTestResultRepository
}

func newGlobalTestService(repo ScheduledTestPlanRepository) *ScheduledTestService {
	return NewScheduledTestService(repo, &stubGlobalResultRepo{})
}

// 复用包内既有的 boolPtr 辅助函数（ops_metrics_collector.go）。

func TestUpdateGlobalPlan_RejectsInvalidCron(t *testing.T) {
	svc := newGlobalTestService(&stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1}})
	_, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{CronExpression: "not-a-cron"}, boolPtr(true))
	if err == nil {
		t.Fatalf("expected invalid cron to be rejected")
	}
}

func TestUpdateGlobalPlan_NormalizesAndComputesNextRun(t *testing.T) {
	repo := &stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1, MaxResults: 10, Enabled: true}}
	svc := newGlobalTestService(repo)

	_, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{" anthropic ": " claude-sonnet-4 ", "openai": "  ", "": "x"},
	}, boolPtr(true))
	if err == nil {
		t.Fatalf("expected empty platform key to be rejected")
	}

	updated, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{" anthropic ": " claude-sonnet-4 ", "openai": "  "},
	}, boolPtr(true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.PlatformModels["anthropic"] != "claude-sonnet-4" {
		t.Fatalf("platform/model not trimmed: %+v", updated.PlatformModels)
	}
	if _, ok := updated.PlatformModels["openai"]; ok {
		t.Fatalf("empty model should be dropped: %+v", updated.PlatformModels)
	}
	if updated.NextRunAt == nil || updated.NextRunAt.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("next_run_at not computed: %+v", updated.NextRunAt)
	}
}

func TestUpdateGlobalPlan_DefaultsAndCapsMaxResults(t *testing.T) {
	repo := &stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1, Enabled: false}}
	svc := newGlobalTestService(repo)

	updated, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 0 * * *",
		MaxResults:     0,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.MaxResults != 50 {
		t.Fatalf("expected default max_results=50, got %d", updated.MaxResults)
	}

	_, err = svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 0 * * *",
		MaxResults:     maxGlobalMaxResults + 1,
	}, nil)
	if err == nil {
		t.Fatalf("expected oversized max_results to be rejected")
	}
}

func TestUpdateGlobalPlan_EnabledNilPreservesState(t *testing.T) {
	repo := &stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1, Enabled: true}}
	svc := newGlobalTestService(repo)

	updated, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{"anthropic": "claude-sonnet-4"},
		// 未显式传 enabled：保留现有开启状态
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updated.Enabled {
		t.Fatalf("expected enabled state to be preserved")
	}
}

func TestUpdateGlobalPlan_EnabledRequiresPlatformModel(t *testing.T) {
	repo := &stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1, Enabled: false}}
	svc := newGlobalTestService(repo)

	// 启用但所有平台模型为空：拒绝保存
	_, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{"anthropic": "  "},
	}, boolPtr(true))
	if err == nil {
		t.Fatalf("expected enabling without any platform model to be rejected")
	}

	// 关闭时空模型允许保存
	_, err = svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{"anthropic": "  "},
	}, boolPtr(false))
	if err != nil {
		t.Fatalf("expected disabling with empty models to succeed: %v", err)
	}
}

func TestUpdateGlobalPlan_InputLimits(t *testing.T) {
	svc := newGlobalTestService(&stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1}})

	tooMany := make(map[string]string, maxGlobalPlatformModelEntries+1)
	for i := 0; i < maxGlobalPlatformModelEntries+1; i++ {
		tooMany["platform_"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "m"
	}
	if _, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{CronExpression: "0 * * * *", PlatformModels: tooMany}, boolPtr(false)); err == nil {
		t.Fatalf("expected too many platform entries to be rejected")
	}

	longModel := strings.Repeat("m", maxGlobalModelIDLength+1)
	if _, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{"anthropic": longModel},
	}, boolPtr(false)); err == nil {
		t.Fatalf("expected oversized model id to be rejected")
	}
}

func TestDeletePlan_RejectsGlobalRow(t *testing.T) {
	globalID := int64(7)
	repo := &stubGlobalPlanRepo{
		global: &ScheduledTestPlan{ID: globalID},
		byID: map[int64]*ScheduledTestPlan{
			globalID: {ID: globalID},
			9:        {ID: 9, AccountID: func() *int64 { v := int64(42); return &v }()},
		},
	}
	svc := newGlobalTestService(repo)

	if err := svc.DeletePlan(context.Background(), globalID); err == nil {
		t.Fatalf("expected deleting global plan to fail")
	}
	if err := svc.DeletePlan(context.Background(), 9); err != nil {
		t.Fatalf("expected per-account delete to succeed: %v", err)
	}
}

func TestNormalizePlatformModels(t *testing.T) {
	out, err := normalizePlatformModels(map[string]string{"a": "m1", "b": " ", "c": "m2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out["a"] != "m1" || out["c"] != "m2" {
		t.Fatalf("unexpected result: %+v", out)
	}
	if _, err := normalizePlatformModels(map[string]string{"": "m"}); err == nil {
		t.Fatalf("expected empty platform to be rejected")
	}
}

// TestUpdateGlobalPlan_DefaultsAndCapsPacingParams 验证削峰参数的归一化：
// 并发未传/非正数回落默认 3，间隔负数归 0（突发模式），超上限显式拒绝。
func TestUpdateGlobalPlan_DefaultsAndCapsPacingParams(t *testing.T) {
	repo := &stubGlobalPlanRepo{global: &ScheduledTestPlan{ID: 1, Enabled: false}}
	svc := newGlobalTestService(repo)

	updated, err := svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression:          "0 0 * * *",
		MaxWorkers:              0,
		DispatchIntervalSeconds: -5,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.MaxWorkers != defaultGlobalMaxWorkers {
		t.Fatalf("expected default max_workers=%d, got %d", defaultGlobalMaxWorkers, updated.MaxWorkers)
	}
	if updated.DispatchIntervalSeconds != 0 {
		t.Fatalf("expected negative dispatch interval to normalize to 0, got %d", updated.DispatchIntervalSeconds)
	}

	if _, err = svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression: "0 0 * * *",
		MaxWorkers:     maxGlobalMaxWorkers + 1,
	}, nil); err == nil {
		t.Fatalf("expected oversized max_workers to be rejected")
	}
	if _, err = svc.UpdateGlobalPlan(context.Background(), &ScheduledTestPlan{
		CronExpression:          "0 0 * * *",
		DispatchIntervalSeconds: maxGlobalDispatchIntervalSeconds + 1,
	}, nil); err == nil {
		t.Fatalf("expected oversized dispatch interval to be rejected")
	}
}

// TestComputeGlobalBatchTimeout 表驱动验证自适应批次超时：
// 下限保持旧 30 分钟语义，区间内线性，超上限截断为 6 小时。
func TestComputeGlobalBatchTimeout(t *testing.T) {
	cases := []struct {
		name     string
		targets  int
		interval int
		want     time.Duration
	}{
		{"无目标回落30分钟下限", 0, 0, scheduledTestMinBatchTimeout},
		{"小批次保持下限", 3, 1, scheduledTestMinBatchTimeout},
		{"线性区间取计算值", 200, 10, 200*10*time.Second + scheduledTestPerTestBudget},
		{"超上限截断为6小时", 10000, 600, scheduledTestMaxBatchTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeGlobalBatchTimeout(tc.targets, tc.interval); got != tc.want {
				t.Fatalf("computeGlobalBatchTimeout(%d, %d) = %s, want %s", tc.targets, tc.interval, got, tc.want)
			}
		})
	}
}

// stubGlobalResultCreateRepo 空实现结果仓储写入，供派发行为测试使用；
// 各测试自行用局部计数器断言，桩本身保持无状态。
type stubGlobalResultCreateRepo struct {
	ScheduledTestResultRepository
}

func (s *stubGlobalResultCreateRepo) Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error) {
	return result, nil
}

func (s *stubGlobalResultCreateRepo) PruneOldResults(ctx context.Context, planID int64, keepCount int) error {
	return nil
}

// fixedAccountSource 固定返回指定候选账号，供派发行为测试使用。
type fixedAccountSource struct {
	accounts []ScheduledTestAccount
}

func (s fixedAccountSource) ListGlobalScheduledTestCandidates(ctx context.Context) ([]ScheduledTestAccount, error) {
	return s.accounts, nil
}

// newDispatchTestRunner 构造可操控全局批次执行行为的 runner：
// 账号源与单次测试执行均可注入，用于验证派发节奏/并发上限/截断。
func newDispatchTestRunner(repo *claimRecorder, resultRepo ScheduledTestResultRepository, src fixedAccountSource, runTest func(context.Context, int64, string) (*ScheduledTestResult, error)) *ScheduledTestRunnerService {
	runner := NewScheduledTestRunnerService(repo, NewScheduledTestService(repo, resultRepo), nil, nil, nil, nil)
	runner.accountSrc = src
	runner.runTest = runTest
	return runner
}

// fixedAccounts 生成 n 个指定平台的候选账号。
func fixedAccounts(n int, platform string) []ScheduledTestAccount {
	accounts := make([]ScheduledTestAccount, 0, n)
	for i := 0; i < n; i++ {
		accounts = append(accounts, ScheduledTestAccount{ID: int64(100 + i), Platform: platform})
	}
	return accounts
}

// TestRunGlobalPlan_DispatchIntervalPacesDispatch 验证派发间隔摊开：
// 相邻测试的实际开始间隔不小于配置的派发间隔，整批被拉长为时间窗。
func TestRunGlobalPlan_DispatchIntervalPacesDispatch(t *testing.T) {
	const targets = 3
	var mu sync.Mutex
	var starts []time.Time
	runner := newDispatchTestRunner(&claimRecorder{}, &stubGlobalResultCreateRepo{},
		fixedAccountSource{accounts: fixedAccounts(targets, "anthropic")},
		func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
			return &ScheduledTestResult{Status: "success"}, nil
		})
	plan := &ScheduledTestPlan{
		ID:                      1,
		CronExpression:          "0 * * * *",
		PlatformModels:          map[string]string{"anthropic": "m"},
		MaxWorkers:              targets,
		DispatchIntervalSeconds: 1,
	}

	runStart := time.Now()
	runner.runGlobalPlan(plan)

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != targets {
		t.Fatalf("expected %d dispatched tests, got %d", targets, len(starts))
	}
	// 首个目标立即派发，其余每个目标前等待一个间隔：
	// 总跨度应 ≥ (targets-1) × interval（留 100ms 调度容差）。
	tolerance := 100 * time.Millisecond
	wantSpan := time.Duration(targets-1) * time.Second
	if span := starts[len(starts)-1].Sub(starts[0]); span < wantSpan-tolerance {
		t.Fatalf("dispatch span %s shorter than paced span %s", span, wantSpan-tolerance)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < time.Second-tolerance {
			t.Fatalf("dispatch gap %d = %s, want >= %s", i, gap, time.Second-tolerance)
		}
	}
	if elapsed := time.Since(runStart); elapsed < wantSpan-tolerance {
		t.Fatalf("runGlobalPlan returned too early (%s), batch must span the pacing window", elapsed)
	}
}

// TestRunGlobalPlan_ConcurrencyCapRespected 验证并发上限：
// interval=0 的突发模式下，同时在跑的测试数不超过 max_workers，
// 且确实达到了上限（证明并发执行真实发生）。
func TestRunGlobalPlan_ConcurrencyCapRespected(t *testing.T) {
	const targets = 6
	const workers = 2
	var mu sync.Mutex
	active, peak, executed := 0, 0, 0
	runner := newDispatchTestRunner(&claimRecorder{}, &stubGlobalResultCreateRepo{},
		fixedAccountSource{accounts: fixedAccounts(targets, "anthropic")},
		func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
			mu.Lock()
			active++
			if active > peak {
				peak = active
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			active--
			executed++
			mu.Unlock()
			return &ScheduledTestResult{Status: "success"}, nil
		})
	plan := &ScheduledTestPlan{
		ID:             1,
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{"anthropic": "m"},
		MaxWorkers:     workers,
	}

	runner.runGlobalPlan(plan)

	mu.Lock()
	defer mu.Unlock()
	if executed != targets {
		t.Fatalf("expected %d executed tests, got %d", targets, executed)
	}
	if peak != workers {
		t.Fatalf("expected peak concurrency exactly %d, got %d", workers, peak)
	}
}

// TestRunGlobalPlan_BatchTimeoutStopsDispatch 验证批次超时截断：
// ctx 到期后停止派发剩余目标，已派发目标正常收尾，不重复、不卡死。
func TestRunGlobalPlan_BatchTimeoutStopsDispatch(t *testing.T) {
	const targets = 10
	var mu sync.Mutex
	dispatched := 0
	seen := map[int64]bool{}
	runner := newDispatchTestRunner(&claimRecorder{}, &stubGlobalResultCreateRepo{},
		fixedAccountSource{accounts: fixedAccounts(targets, "anthropic")},
		func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
			mu.Lock()
			dispatched++
			seen[accountID] = true
			mu.Unlock()
			return &ScheduledTestResult{Status: "success"}, nil
		})
	// 注入 500ms 短批次超时：首个目标立即派发，第二个目标等待 interval
	// （1s）期间 ctx 到期，派发循环退出。
	runner.batchTimeoutFn = func(targetCount, intervalSeconds int) time.Duration {
		return 500 * time.Millisecond
	}
	plan := &ScheduledTestPlan{
		ID:                      1,
		CronExpression:          "0 * * * *",
		PlatformModels:          map[string]string{"anthropic": "m"},
		MaxWorkers:              targets,
		DispatchIntervalSeconds: 1,
	}

	runStart := time.Now()
	runner.runGlobalPlan(plan)

	mu.Lock()
	defer mu.Unlock()
	// 极端调度延迟下阻塞 select 两路同时就绪会随机选择，可能多派发
	// 极少量目标；放宽为“至多 2 个且都在列表头部”，仍足以证明截断发生。
	if dispatched > 2 {
		t.Fatalf("expected at most 2 dispatched targets after ctx done, got %d", dispatched)
	}
	if len(seen) > 2 || !seen[100] {
		t.Fatalf("expected only the first account(s) (100, 101) to be tested, got %v", seen)
	}
	if elapsed := time.Since(runStart); elapsed > 2*time.Second {
		t.Fatalf("runGlobalPlan should return promptly after ctx done, took %s", elapsed)
	}
	if runner.globalRunning.Load() {
		t.Fatalf("expected globalRunning to be released after truncated batch")
	}
}

// TestRunGlobalPlan_BurstModeStopsAfterTimeout 回归：interval=0（突发模式）
// 下派发循环没有阻塞等待点，批次超时必须仍能停止派发，而不是把剩余
// 目标全量派发（产生日志风暴与瞬时 goroutine churn）。
func TestRunGlobalPlan_BurstModeStopsAfterTimeout(t *testing.T) {
	const targets = 20
	var mu sync.Mutex
	dispatched := 0
	runner := newDispatchTestRunner(&claimRecorder{}, &stubGlobalResultCreateRepo{},
		fixedAccountSource{accounts: fixedAccounts(targets, "anthropic")},
		func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
			mu.Lock()
			dispatched++
			mu.Unlock()
			// 占住唯一的并发槽直到 ctx 到期：保证主派发循环阻塞在
			// 信号量上直到超时，之后的 ctx 检查必须拦住后续派发。
			<-ctx.Done()
			return nil, ctx.Err()
		})
	runner.batchTimeoutFn = func(targetCount, intervalSeconds int) time.Duration {
		return 200 * time.Millisecond
	}
	plan := &ScheduledTestPlan{
		ID:             1,
		CronExpression: "0 * * * *",
		PlatformModels: map[string]string{"anthropic": "m"},
		MaxWorkers:     1,
		// interval 零值 = 突发模式：修复前循环内没有任何 ctx 检查
	}

	runner.runGlobalPlan(plan)

	mu.Lock()
	defer mu.Unlock()
	// 目标 0 立即派发并占槽；目标 1 在超时、信号量释放后派发；
	// 目标 2 起被非阻塞 ctx 检查拦住。
	if dispatched != 2 {
		t.Fatalf("expected exactly 2 dispatched targets in burst mode after ctx done, got %d", dispatched)
	}
	if runner.globalRunning.Load() {
		t.Fatalf("expected globalRunning to be released after truncated batch")
	}
}

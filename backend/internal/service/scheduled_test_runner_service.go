package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const scheduledTestDefaultMaxWorkers = 10

// scheduledTestGlobalListTimeout 候选账号列表查询超时：批次超时依赖目标数，
// 需先拿到列表才能计算，因此列表查询使用独立短超时。
const scheduledTestGlobalListTimeout = 5 * time.Minute

// 全局批次自适应超时的三个基准：
//   - perTestBudget：单次测试的耗时余量（真实流式调用通常秒级完成，
//     慢上游也留足冗余）；批次超时 = 派发总时长（间隔 × 目标数）+ 该余量；
//   - minBatchTimeout：下限保持旧的固定 30 分钟语义，间隔为 0 或目标
//     很少时批次行为与改造前完全一致；
//   - maxBatchTimeout：上限防呆。管理员把间隔/账号数配得极大时，批次
//     到上限被截断，下一轮 cron 将对全部候选账号重新调度测试（候选查询
//     无“本轮已测”过滤，非断点续测；ClaimForRun 已推进 next_run_at，
//     不会重复触发）。
const (
	scheduledTestPerTestBudget   = 10 * time.Minute
	scheduledTestMinBatchTimeout = 30 * time.Minute
	scheduledTestMaxBatchTimeout = 6 * time.Hour
)

// scheduledTestBatchTruncatedLogFmt 批次因 ctx 到期被截断的日志：
// 派发停止，下一轮 cron 将重新调度全部候选账号（非断点续测）。
const scheduledTestBatchTruncatedLogFmt = "[ScheduledTestRunner] global plan=%d batch ctx done after dispatching %d/%d targets; all candidate accounts will be re-tested next round"

// ScheduledTestRunnerService periodically scans due test plans and executes them.
type ScheduledTestRunnerService struct {
	planRepo       ScheduledTestPlanRepository
	scheduledSvc   *ScheduledTestService
	accountTestSvc *AccountTestService
	rateLimitSvc   *RateLimitService
	cfg            *config.Config
	// accountRepo 用于失败联动时加载完整账号（含临时不可调度规则配置）。
	accountRepo AccountRepository
	// accountSrc 提供全局定时测试的候选账号（所有非禁用账号）。
	accountSrc ScheduledTestAccountSource
	// runTest 执行单次账号测试。收敛为函数字段：生产路径在构造时绑定
	// accountTestSvc.RunTestBackground，单元测试可替换为时序/并发桩，
	// 从而验证派发间隔与并发上限的真实行为。
	runTest func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error)
	// batchTimeoutFn 计算全局批次自适应超时；独立函数字段便于单测注入
	// 短超时验证截断行为，生产路径绑定 computeGlobalBatchTimeout。
	batchTimeoutFn func(targetCount, intervalSeconds int) time.Duration
	// globalRunning 进程内互斥标记：高频 cron + 大账号量时单批次可能超过
	// cron 间隔，ClaimForRun 只防同一到期点重复认领，这里防止上一批次
	// 未完成时新批次并发测同一账号。
	globalRunning atomic.Bool

	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewScheduledTestRunnerService creates a new runner.
func NewScheduledTestRunnerService(
	planRepo ScheduledTestPlanRepository,
	scheduledSvc *ScheduledTestService,
	accountTestSvc *AccountTestService,
	rateLimitSvc *RateLimitService,
	cfg *config.Config,
	accountRepo AccountRepository,
) *ScheduledTestRunnerService {
	s := &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   scheduledSvc,
		accountTestSvc: accountTestSvc,
		rateLimitSvc:   rateLimitSvc,
		cfg:            cfg,
		accountRepo:    accountRepo,
	}
	// 全局批次单测入口绑定：svc 为 nil（既有测试传 nil，只走跳过路径）
	// 时绑定显式报错兜底，避免运行期空指针。
	if accountTestSvc != nil {
		s.runTest = accountTestSvc.RunTestBackground
	} else {
		s.runTest = func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
			return nil, fmt.Errorf("account test service unavailable")
		}
	}
	s.batchTimeoutFn = computeGlobalBatchTimeout
	// 通过类型断言获取候选账号查询能力，避免扩张宽泛的账号仓储接口；
	// 断言失败属装配缺陷，启动期即大声报错，而不是运行期静默降级。
	if src, ok := accountRepo.(ScheduledTestAccountSource); ok {
		s.accountSrc = src
	} else {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] account repository does not implement ScheduledTestAccountSource; global scheduled tests will be skipped")
	}
	return s
}

// Start begins the cron ticker (every minute).
func (s *ScheduledTestRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}

		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		_, err := c.AddFunc("* * * * *", func() { s.runScheduled() })
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] not started (invalid schedule): %v", err)
			return
		}
		s.cron = c
		s.cron.Start()
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] started (tick=every minute)")
	})
}

// Stop gracefully shuts down the cron scheduler.
func (s *ScheduledTestRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron != nil {
			ctx := s.cron.Stop()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] cron stop timed out")
			}
		}
	})
}

func (s *ScheduledTestRunnerService) runScheduled() {
	// Delay 10s so execution lands at ~:10 of each minute instead of :00.
	time.Sleep(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	now := time.Now()
	plans, err := s.planRepo.ListDue(ctx, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] ListDue error: %v", err)
		return
	}
	if len(plans) == 0 {
		return
	}

	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] found %d due plans", len(plans))

	sem := make(chan struct{}, scheduledTestDefaultMaxWorkers)
	var wg sync.WaitGroup

	for _, plan := range plans {
		// 全局计划行单独走 runGlobalPlan：不绑定账号，按平台模型批量测试。
		if plan.AccountID == nil {
			wg.Add(1)
			go func(p *ScheduledTestPlan) {
				defer wg.Done()
				s.runGlobalPlan(p)
			}(plan)
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p *ScheduledTestPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runOnePlan(ctx, p)
		}(plan)
	}

	wg.Wait()
}

// runGlobalPlan 对所有非禁用账号执行一轮全局定时测试：
// 按账号平台取 platform_models 中配置的测试模型，未配置模型的平台跳过；
// 结果仍按账号写入 scheduled_test_results（account_id 冗余），便于回看。
//
// 执行前先通过 ClaimForRun 原子推进 next_run_at：全局批次超时自适应
// （30 分钟~6 小时，见 computeGlobalBatchTimeout），若沿用“执行完再推进”
// 会在批次进行期间被每分钟 tick（或多实例）重复触发。
// 进程内再用 globalRunning 互斥：高频 cron 下上一批次未完成时，新到期的
// 批次直接跳过，避免对同一账号并发测试/自动恢复。
func (s *ScheduledTestRunnerService) runGlobalPlan(plan *ScheduledTestPlan) {
	if !s.globalRunning.CompareAndSwap(false, true) {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d skipped: previous global batch still running", plan.ID)
		return
	}
	defer s.globalRunning.Store(false)

	now := time.Now()
	nextRun, err := computeNextRun(plan.CronExpression, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d computeNextRun error: %v", plan.ID, err)
		return
	}
	claimed, err := s.planRepo.ClaimForRun(context.Background(), plan.ID, now, nextRun)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d ClaimForRun error: %v", plan.ID, err)
		return
	}
	if !claimed {
		// 已被其他 tick/实例认领，本轮跳过。
		return
	}

	if s.accountSrc == nil {
		// 装配缺陷：accountRepository 未实现候选账号查询。已认领本轮作废，
		// 只记录错误日志等待下一周期，避免每分钟刷屏。
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d skipped: account source unavailable (wiring defect)", plan.ID)
		return
	}

	// 候选账号查询使用独立短超时：批次超时依赖目标数，需先拿到列表。
	listCtx, cancelList := context.WithTimeout(context.Background(), scheduledTestGlobalListTimeout)
	defer cancelList()
	accounts, err := s.accountSrc.ListGlobalScheduledTestCandidates(listCtx)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d ListGlobalScheduledTestCandidates error: %v", plan.ID, err)
		return
	}

	type target struct {
		accountID int64
		modelID   string
	}
	targets := make([]target, 0, len(accounts))
	skipped := 0
	for _, acc := range accounts {
		modelID, ok := plan.PlatformModels[acc.Platform]
		if !ok {
			skipped++
			continue
		}
		targets = append(targets, target{accountID: acc.ID, modelID: modelID})
	}

	// 削峰参数归一化：workers<=0（未迁移行/测试构造）回落保守默认 3；
	// 间隔负值归 0——0 是合法的突发配置，不回落 5（默认 5 由迁移列
	// 默认值与保存时归一化保证，runner 只防御非法负值）。
	workers := plan.MaxWorkers
	if workers <= 0 {
		workers = defaultGlobalMaxWorkers
	}
	intervalSeconds := plan.DispatchIntervalSeconds
	if intervalSeconds < 0 {
		intervalSeconds = 0
	}
	interval := time.Duration(intervalSeconds) * time.Second

	// 自适应批次超时：派发总时长 + 单测预算，小批次保持旧 30 分钟语义，
	// 超大批次到上限截断（下一轮 cron 全量重测）。一次计算供 ctx 与
	// 日志共用，避免注入桩时日志口径与实际超时不一致。
	batchTimeout := s.batchTimeoutFn(len(targets), intervalSeconds)
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()

	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d: %d accounts to test, %d skipped (platform model not configured), workers=%d, dispatch interval=%ds, batch timeout=%s", plan.ID, len(targets), skipped, workers, intervalSeconds, batchTimeout)

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
dispatch:
	for i, t := range targets {
		// 派发间隔摊开：除首个目标外，每个目标派发前等待 interval。
		// 等待放在抢信号量之前——节奏控制的是“提交速率”，而不是占着
		// 并发槽空等；ctx 到期（自适应超时/进程停止）时停止派发，
		// 已派发目标继续跑完，下一轮 cron 将重新调度全部候选账号。
		if i > 0 && interval > 0 {
			select {
			case <-ctx.Done():
				logger.LegacyPrintf("service.scheduled_test_runner", scheduledTestBatchTruncatedLogFmt, plan.ID, i, len(targets))
				break dispatch
			case <-time.After(interval):
			}
		} else if ctx.Err() != nil {
			// 非阻塞 ctx 检查：interval=0（突发模式）或首个目标时循环内
			// 没有等待点，必须在此拦住，否则批次超时后剩余目标仍会被
			// 全量派发，产生日志风暴与瞬时 goroutine churn。
			logger.LegacyPrintf("service.scheduled_test_runner", scheduledTestBatchTruncatedLogFmt, plan.ID, i, len(targets))
			break dispatch
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(tg target) {
			defer wg.Done()
			defer func() { <-sem }()
			result, err := s.runTest(ctx, tg.accountID, tg.modelID)
			if err != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d account=%d RunTestBackground error: %v", plan.ID, tg.accountID, err)
				return
			}
			result.AccountID = &tg.accountID
			if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d account=%d SaveResult error: %v", plan.ID, tg.accountID, err)
			}
			s.applySchedulingSideEffects(ctx, tg.accountID, plan.ID, tg.modelID, result)
			if result.Status == "success" && plan.AutoRecover {
				s.tryRecoverAccount(ctx, tg.accountID, plan.ID)
			}
		}(t)
	}
	wg.Wait()
}

// computeGlobalBatchTimeout 计算全局批次的自适应超时：
// 派发总时长（间隔 × 目标数）+ 单测预算余量。下限保持旧的 30 分钟
// 固定语义；上限 6 小时防呆，超限批次被截断后由下一轮 cron 全量重测。
func computeGlobalBatchTimeout(targetCount, intervalSeconds int) time.Duration {
	batch := time.Duration(intervalSeconds)*time.Second*time.Duration(targetCount) + scheduledTestPerTestBudget
	if batch < scheduledTestMinBatchTimeout {
		return scheduledTestMinBatchTimeout
	}
	if batch > scheduledTestMaxBatchTimeout {
		return scheduledTestMaxBatchTimeout
	}
	return batch
}

func (s *ScheduledTestRunnerService) runOnePlan(ctx context.Context, plan *ScheduledTestPlan) {
	// 按账号计划必然携带账号 ID；防御空值避免空指针。
	if plan.AccountID == nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d missing account id", plan.ID)
		return
	}
	result, err := s.accountTestSvc.RunTestBackground(ctx, *plan.AccountID, plan.ModelID)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d RunTestBackground error: %v", plan.ID, err)
		return
	}

	// 冗余记录被测账号，保证按账号查询结果时历史数据完整。
	result.AccountID = plan.AccountID
	if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d SaveResult error: %v", plan.ID, err)
	}

	s.applySchedulingSideEffects(ctx, *plan.AccountID, plan.ID, plan.ModelID, result)

	// Auto-recover account if test succeeded and auto_recover is enabled.
	// 只负责 status=error 等账号级恢复；临时调度限制已在上面解耦处理。
	if result.Status == "success" && plan.AutoRecover {
		s.tryRecoverAccount(ctx, *plan.AccountID, plan.ID)
	}

	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d computeNextRun error: %v", plan.ID, err)
		return
	}

	if err := s.planRepo.UpdateAfterRun(ctx, plan.ID, time.Now(), nextRun); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d UpdateAfterRun error: %v", plan.ID, err)
	}
}

// applySchedulingSideEffects 将一次定时测试结果联动到实际调度状态，形成
// “失败熔断、成功恢复”闭环。定时测试直接请求指定账号（绕过业务调度层的
// cooldown），因此它比业务流量更早发现额度耗尽与额度恢复：
//   - 失败且保留有结构化 HTTP 上下文时，复用真实流量的临时不可调度入口
//     HandleTempUnschedulable（含 pool-mode / 自定义错误码白名单门控），
//     且刻意不传模型名：真实流量传入模型名会走模型级 cooldown，但定时
//     测试代表的是“该账号当前不可用”的强信号（额度耗尽等账号级状态），
//     命中规则应对整个账号临时不可调度，而不是只停被测模型。未命中规则
//     时只记录失败，不产生调度副作用。
//     影响面（账号级语义的固有取舍，管理员配置规则时应知悉）：
//     spark 影子账号会因母账号 temp-unsched 连坐停调（影子共享母配额，
//     语义自洽）；任一模型的定时测试成功即解封整账号（账号级状态不携带
//     模型归属）。
//   - 成功时立即清除由临时错误策略产生的调度限制，不等待原 TTL 自然过期。
//     该恢复与 AutoRecover 开关解耦，且只处理临时状态：手动禁用、
//     status=error、永久错误不受影响。
//
// planModelID 是计划侧配置的被测模型：用于成功恢复时精确清除该模型的
// model_rate_limits key（真实流量会写模型级 cooldown），由恢复入口统一做
// 模型名映射，保证清除的 key 与写入侧同口径。
func (s *ScheduledTestRunnerService) applySchedulingSideEffects(ctx context.Context, accountID int64, planID int64, planModelID string, result *ScheduledTestResult) {
	if s.rateLimitSvc == nil || result == nil {
		return
	}

	if result.Status != "success" {
		// 无结构化 HTTP 上下文（网络失败、非 HTTP 测试等）：只保留失败记录，
		// 不猜测错误语义，避免误触发临时不可调度。
		if result.StatusCode <= 0 {
			return
		}
		// 401 在测试路径已有完整处置（apikey 直接 SetError 永久标记），
		// 再叠加临时不可调度会让 error 账号挂着 temp-unsched 状态混杂，
		// 跳过联动交由既有语义处理。
		if result.StatusCode == http.StatusUnauthorized {
			return
		}
		if s.accountRepo == nil {
			return
		}
		account, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d account=%d load account for error policy failed: %v", planID, accountID, err)
			return
		}
		// 复用真实流量入口 HandleTempUnschedulable（不传模型名 → 账号级：
		// SetTempUnschedulable + Redis 缓存 + 调度阻断通知，整账号暂停调度
		// 直到 TTL 或测试成功恢复），确保与真实流量共享同一套门控口径。
		if s.rateLimitSvc.HandleTempUnschedulable(ctx, account, result.StatusCode, result.ResponseBody) {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d account=%d upstream error %d hit temp-unschedulable rule, account temp-unschedulable", planID, accountID, result.StatusCode)
		}
		return
	}

	// 测试成功：恢复判定基于真实生成接口的有效 2xx（RunTestBackground 的
	// success 仅在上游 200 且流式解析成功时产生）。传入计划侧模型，
	// 由恢复入口完成模型名映射后精确清除。
	recoverModelID := result.ModelID
	if recoverModelID == "" {
		recoverModelID = strings.TrimSpace(planModelID)
	}
	if err := s.rateLimitSvc.RecoverTemporarySchedulingStateAfterSuccess(ctx, accountID, recoverModelID); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d account=%d recover temporary scheduling state failed: %v", planID, accountID, err)
	}
}

// tryRecoverAccount attempts to recover an account from recoverable runtime state.
func (s *ScheduledTestRunnerService) tryRecoverAccount(ctx context.Context, accountID int64, planID int64) {
	if s.rateLimitSvc == nil {
		return
	}

	recovery, err := s.rateLimitSvc.RecoverAccountAfterSuccessfulTest(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover failed: %v", planID, err)
		return
	}
	if recovery == nil {
		return
	}

	if recovery.ClearedError {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d recovered from error status", planID, accountID)
	}
	if recovery.ClearedRateLimit {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d cleared rate-limit/runtime state", planID, accountID)
	}
}

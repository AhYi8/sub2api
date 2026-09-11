package service

import (
	"context"
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

// scheduledTestGlobalTimeout 全局批次执行超时：账号量可能远大于按账号计划，
// 使用独立于单轮 tick（5 分钟）的更长上限，避免大批账号被中途截断。
const scheduledTestGlobalTimeout = 30 * time.Minute

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
// 执行前先通过 ClaimForRun 原子推进 next_run_at：全局批次最长 30 分钟，
// 若沿用“执行完再推进”会在批次进行期间被每分钟 tick（或多实例）重复触发。
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

	// 独立超时：全局批次账号量大，不受单轮 tick 的 5 分钟限制。
	ctx, cancel := context.WithTimeout(context.Background(), scheduledTestGlobalTimeout)
	defer cancel()

	accounts, err := s.accountSrc.ListGlobalScheduledTestCandidates(ctx)
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
	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] global plan=%d: %d accounts to test, %d skipped (platform model not configured)", plan.ID, len(targets), skipped)

	sem := make(chan struct{}, scheduledTestDefaultMaxWorkers)
	var wg sync.WaitGroup
	for _, t := range targets {
		sem <- struct{}{}
		wg.Add(1)
		go func(tg target) {
			defer wg.Done()
			defer func() { <-sem }()
			result, err := s.accountTestSvc.RunTestBackground(ctx, tg.accountID, tg.modelID)
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
//   - 失败且保留有结构化 HTTP 上下文时，只应用管理员显式配置的“错误码 +
//     关键词 + 持续时间”临时不可调度规则（与正式链路同一 tryTempUnschedulable
//     实现，已知模型时模型级 cooldown 优先）。刻意不复用 HandleUpstreamError
//     的全量语义：其 401/402/403 兜底（OAuth 冷却、SetError 永久禁用、
//     openai 403 计数等）面向真实流量，被周期性的定时测试反复触发会放大成
//     真实流量不会出现的处罚；未命中规则时只记录失败，不产生调度副作用。
//   - 成功时立即清除由临时错误策略产生的调度限制，不等待原 TTL 自然过期。
//     该恢复与 AutoRecover 开关解耦，且只处理临时状态：手动禁用、
//     status=error、永久错误不受影响。
//
// planModelID 是计划侧配置的被测模型：结构化错误上下文仅在失败时透传
// ModelID（已是映射后的上游模型名），成功结果为空，因此成功恢复必须回退
// 到计划侧模型，并在恢复入口统一做模型名映射，保证清除的 key 与失败时
// 写入的 model_rate_limits key 同口径。
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
		modelID := result.ModelID
		if modelID == "" {
			// 回退到计划侧模型时应用账号模型映射，与真实转发的 cooldown key 口径一致。
			modelID = strings.TrimSpace(planModelID)
			if mapped := strings.TrimSpace(account.GetMappedModel(modelID)); mapped != "" {
				modelID = mapped
			}
		}
		if s.rateLimitSvc.tryTempUnschedulable(ctx, account, result.StatusCode, result.ResponseBody, modelID) {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d account=%d upstream error %d hit temp-unschedulable rule (model=%s)", planID, accountID, result.StatusCode, modelID)
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

package service

import (
	"context"
	"sync"
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
	// accountSrc 提供全局定时测试的候选账号（所有非禁用账号）。
	accountSrc ScheduledTestAccountSource

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
func (s *ScheduledTestRunnerService) runGlobalPlan(plan *ScheduledTestPlan) {
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

	// Auto-recover account if test succeeded and auto_recover is enabled.
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

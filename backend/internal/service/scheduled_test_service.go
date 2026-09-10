package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var scheduledTestCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ScheduledTestService provides CRUD operations for scheduled test plans and results.
type ScheduledTestService struct {
	planRepo   ScheduledTestPlanRepository
	resultRepo ScheduledTestResultRepository
}

// NewScheduledTestService creates a new ScheduledTestService.
func NewScheduledTestService(
	planRepo ScheduledTestPlanRepository,
	resultRepo ScheduledTestResultRepository,
) *ScheduledTestService {
	return &ScheduledTestService{
		planRepo:   planRepo,
		resultRepo: resultRepo,
	}
}

// CreatePlan validates the cron expression, computes next_run_at, and persists the plan.
func (s *ScheduledTestService) CreatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun

	if plan.MaxResults <= 0 {
		plan.MaxResults = 50
	}

	return s.planRepo.Create(ctx, plan)
}

// GetPlan retrieves a plan by ID.
func (s *ScheduledTestService) GetPlan(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

// ListPlansByAccount returns all plans for a given account.
func (s *ScheduledTestService) ListPlansByAccount(ctx context.Context, accountID int64) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListByAccountID(ctx, accountID)
}

// UpdatePlan validates cron and updates the plan.
func (s *ScheduledTestService) UpdatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun

	return s.planRepo.Update(ctx, plan)
}

// DeletePlan removes a plan and its results (via CASCADE).
func (s *ScheduledTestService) DeletePlan(ctx context.Context, id int64) error {
	// 全局保留计划行不允许删除：它是全局定时测试的唯一承载记录。
	plan, err := s.planRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if plan.AccountID == nil {
		return fmt.Errorf("global scheduled test plan cannot be deleted")
	}
	return s.planRepo.Delete(ctx, id)
}

// GetGlobalPlan 返回全局定时测试配置（account_id IS NULL 的保留计划行）。
func (s *ScheduledTestService) GetGlobalPlan(ctx context.Context) (*ScheduledTestPlan, error) {
	return s.planRepo.GetGlobal(ctx)
}

// 全局定时测试输入上限：防止超大 JSONB/结果表无限增长等资源滥用。
const (
	maxGlobalPlatformModelEntries = 32
	maxGlobalPlatformKeyLength    = 64
	maxGlobalModelIDLength        = 256
	maxGlobalMaxResults           = 1000
)

// UpdateGlobalPlan 校验并更新全局定时测试配置：cron、平台模型映射、
// 自动恢复与保留结果数；保存时重算 next_run_at，避免旧周期残留。
// Enabled 传 nil 时保留现有开关状态（PATCH 语义）。
func (s *ScheduledTestService) UpdateGlobalPlan(ctx context.Context, plan *ScheduledTestPlan, enabled *bool) (*ScheduledTestPlan, error) {
	existing, err := s.planRepo.GetGlobal(ctx)
	if err != nil {
		return nil, fmt.Errorf("global scheduled test plan not available (migration 235 required): %w", err)
	}

	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}

	models, err := normalizePlatformModels(plan.PlatformModels)
	if err != nil {
		return nil, err
	}
	// 启用状态下至少配置一个平台模型，否则每轮 0 目标、只有日志，
	// 管理员在 UI 上无感知，直接拒绝保存。
	newEnabled := existing.Enabled
	if enabled != nil {
		newEnabled = *enabled
	}
	if newEnabled && len(models) == 0 {
		return nil, fmt.Errorf("at least one platform model is required when the global scheduled test is enabled")
	}

	existing.CronExpression = plan.CronExpression
	existing.PlatformModels = models
	existing.Enabled = newEnabled
	existing.AutoRecover = plan.AutoRecover
	existing.MaxResults = plan.MaxResults
	if existing.MaxResults <= 0 {
		existing.MaxResults = 50
	}
	if existing.MaxResults > maxGlobalMaxResults {
		return nil, fmt.Errorf("max_results must not exceed %d", maxGlobalMaxResults)
	}
	existing.NextRunAt = &nextRun

	return s.planRepo.Update(ctx, existing)
}

// normalizePlatformModels 清洗平台模型映射：去除空白键值、过滤空模型，
// 并施加条目数与长度上限，防止外部输入滥用存储。
func normalizePlatformModels(models map[string]string) (map[string]string, error) {
	if len(models) > maxGlobalPlatformModelEntries {
		return nil, fmt.Errorf("platform_models supports at most %d entries", maxGlobalPlatformModelEntries)
	}
	out := make(map[string]string, len(models))
	for platform, model := range models {
		p := strings.TrimSpace(platform)
		m := strings.TrimSpace(model)
		if p == "" {
			return nil, fmt.Errorf("platform_models contains empty platform")
		}
		if len(p) > maxGlobalPlatformKeyLength {
			return nil, fmt.Errorf("platform name too long (max %d chars)", maxGlobalPlatformKeyLength)
		}
		if len(m) > maxGlobalModelIDLength {
			return nil, fmt.Errorf("model id too long (max %d chars)", maxGlobalModelIDLength)
		}
		if m == "" {
			continue // 未配置模型的平台直接跳过（该平台账号不会被测试）
		}
		out[p] = m
	}
	return out, nil
}

// ListResultsByAccount 返回某账号最近的测试结果（含全局计划产生的结果）。
func (s *ScheduledTestService) ListResultsByAccount(ctx context.Context, accountID int64, limit int) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByAccountID(ctx, accountID, limit)
}


// ListResults returns the most recent results for a plan.
func (s *ScheduledTestService) ListResults(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit)
}

// SaveResult inserts a result and prunes old entries beyond maxResults.
func (s *ScheduledTestService) SaveResult(ctx context.Context, planID int64, maxResults int, result *ScheduledTestResult) error {
	result.PlanID = planID
	if _, err := s.resultRepo.Create(ctx, result); err != nil {
		return err
	}
	return s.resultRepo.PruneOldResults(ctx, planID, maxResults)
}

func computeNextRun(cronExpr string, from time.Time) (time.Time, error) {
	sched, err := scheduledTestCronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

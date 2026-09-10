package service

import (
	"context"
	"time"
)

// ScheduledTestPlan represents a scheduled test plan domain model.
// AccountID 为 nil 时表示“全局计划行”：不绑定具体账号，
// 由 runner 按平台模型配置（PlatformModels）对所有非禁用账号执行测试。
type ScheduledTestPlan struct {
	ID             int64              `json:"id"`
	AccountID      *int64             `json:"account_id"`
	ModelID        string             `json:"model_id"`
	PlatformModels map[string]string  `json:"platform_models,omitempty"`
	CronExpression string             `json:"cron_expression"`
	Enabled        bool               `json:"enabled"`
	MaxResults     int                `json:"max_results"`
	AutoRecover    bool               `json:"auto_recover"`
	LastRunAt      *time.Time         `json:"last_run_at"`
	NextRunAt      *time.Time         `json:"next_run_at"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// ScheduledTestResult represents a single test execution result.
// AccountID 冗余存储被测账号 ID：全局计划的结果也按账号落库，便于回看。
// IsGlobal 仅在按账号聚合查询时由 JOIN 填充，标识结果来自全局计划。
type ScheduledTestResult struct {
	ID           int64     `json:"id"`
	PlanID       int64     `json:"plan_id"`
	AccountID    *int64    `json:"account_id,omitempty"`
	IsGlobal     bool      `json:"is_global,omitempty"`
	Status       string    `json:"status"`
	ResponseText string    `json:"response_text"`
	ErrorMessage string    `json:"error_message"`
	LatencyMs    int64     `json:"latency_ms"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// ScheduledTestPlanRepository defines the data access interface for test plans.
type ScheduledTestPlanRepository interface {
	Create(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	GetByID(ctx context.Context, id int64) (*ScheduledTestPlan, error)
	GetGlobal(ctx context.Context) (*ScheduledTestPlan, error)
	ListByAccountID(ctx context.Context, accountID int64) ([]*ScheduledTestPlan, error)
	ListDue(ctx context.Context, now time.Time) ([]*ScheduledTestPlan, error)
	Update(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	Delete(ctx context.Context, id int64) error
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error
	// ClaimForRun 原子认领到期计划：仅当 enabled 且 next_run_at <= now 时
	// 推进 last_run_at/next_run_at 并返回 true。用于防止长批次执行期间
	// 被下一轮 tick（或多实例）重复取出执行。
	ClaimForRun(ctx context.Context, id int64, now time.Time, nextRunAt time.Time) (bool, error)
}

// ScheduledTestResultRepository defines the data access interface for test results.
type ScheduledTestResultRepository interface {
	Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error)
	ListByPlanID(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error)
	ListByAccountID(ctx context.Context, accountID int64, limit int) ([]*ScheduledTestResult, error)
	PruneOldResults(ctx context.Context, planID int64, keepCount int) error
}

// ScheduledTestAccount 是全局定时测试的候选账号轻量视图。
type ScheduledTestAccount struct {
	ID       int64
	Platform string
}

// ScheduledTestAccountSource 提供全局定时测试的候选账号（所有非禁用账号）。
// 由 accountRepository 实现并通过类型断言注入 runner，避免扩张宽泛的账号仓储接口。
type ScheduledTestAccountSource interface {
	ListGlobalScheduledTestCandidates(ctx context.Context) ([]ScheduledTestAccount, error)
}

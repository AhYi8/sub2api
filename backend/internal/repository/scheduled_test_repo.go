package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// --- Plan Repository ---

type scheduledTestPlanRepository struct {
	db *sql.DB
}

func NewScheduledTestPlanRepository(db *sql.DB) service.ScheduledTestPlanRepository {
	return &scheduledTestPlanRepository{db: db}
}

const scheduledTestPlanColumns = `id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, platform_models`

func (r *scheduledTestPlanRepository) Create(ctx context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at, platform_models, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		RETURNING `+scheduledTestPlanColumns+`
	`, plan.AccountID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt, platformModelsJSON(plan.PlatformModels))
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) GetByID(ctx context.Context, id int64) (*service.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+scheduledTestPlanColumns+`
		FROM scheduled_test_plans WHERE id = $1
	`, id)
	return scanPlan(row)
}

// GetGlobal 返回全局保留计划行（account_id IS NULL）。
func (r *scheduledTestPlanRepository) GetGlobal(ctx context.Context) (*service.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+scheduledTestPlanColumns+`
		FROM scheduled_test_plans WHERE account_id IS NULL
		LIMIT 1
	`)
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) ListByAccountID(ctx context.Context, accountID int64) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+scheduledTestPlanColumns+`
		FROM scheduled_test_plans WHERE account_id = $1
		ORDER BY created_at DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows)
}

func (r *scheduledTestPlanRepository) ListDue(ctx context.Context, now time.Time) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+scheduledTestPlanColumns+`
		FROM scheduled_test_plans
		WHERE enabled = true AND next_run_at <= $1
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows)
}

func (r *scheduledTestPlanRepository) Update(ctx context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		UPDATE scheduled_test_plans
		SET model_id = $2, cron_expression = $3, enabled = $4, max_results = $5, auto_recover = $6, next_run_at = $7, platform_models = $8, updated_at = NOW()
		WHERE id = $1
		RETURNING `+scheduledTestPlanColumns+`
	`, plan.ID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt, platformModelsJSON(plan.PlatformModels))
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM scheduled_test_plans WHERE id = $1`, id)
	return err
}

func (r *scheduledTestPlanRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_test_plans SET last_run_at = $2, next_run_at = $3, updated_at = NOW() WHERE id = $1
	`, id, lastRunAt, nextRunAt)
	return err
}

// ClaimForRun 原子认领到期计划：条件 UPDATE 保证同一行在同一时刻
// 只被一个执行方（单实例 tick 或多实例）成功推进，防止长批次期间重复触发。
func (r *scheduledTestPlanRepository) ClaimForRun(ctx context.Context, id int64, now time.Time, nextRunAt time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_test_plans
		SET last_run_at = $2, next_run_at = $3, updated_at = NOW()
		WHERE id = $1 AND enabled = true AND next_run_at <= $2
	`, id, now, nextRunAt)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// --- Result Repository ---

type scheduledTestResultRepository struct {
	db *sql.DB
}

func NewScheduledTestResultRepository(db *sql.DB) service.ScheduledTestResultRepository {
	return &scheduledTestResultRepository{db: db}
}

const scheduledTestResultColumns = `id, plan_id, account_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at`

func (r *scheduledTestResultRepository) Create(ctx context.Context, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_results (plan_id, account_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		RETURNING `+scheduledTestResultColumns+`
	`, result.PlanID, result.AccountID, result.Status, result.ResponseText, result.ErrorMessage, result.LatencyMs, result.StartedAt, result.FinishedAt)

	return scanResult(row)
}

func (r *scheduledTestResultRepository) ListByPlanID(ctx context.Context, planID int64, limit int) ([]*service.ScheduledTestResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+scheduledTestResultColumns+`
		FROM scheduled_test_results
		WHERE plan_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, planID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanResults(rows)
}

// ListByAccountID 返回某账号最近的测试结果（含全局计划产生的结果），
// JOIN 计划表填充 is_global，供前端区分结果来源。
func (r *scheduledTestResultRepository) ListByAccountID(ctx context.Context, accountID int64, limit int) ([]*service.ScheduledTestResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.plan_id, r.account_id, (p.account_id IS NULL), r.status, r.response_text, r.error_message, r.latency_ms, r.started_at, r.finished_at, r.created_at
		FROM scheduled_test_results r
		JOIN scheduled_test_plans p ON p.id = r.plan_id
		WHERE r.account_id = $1
		ORDER BY r.created_at DESC
		LIMIT $2
	`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*service.ScheduledTestResult
	for rows.Next() {
		r, err := scanResultWithGlobal(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// PruneOldResults 按 (plan_id, account_id) 分组各保留最近 keepCount 条：
// 按账号计划行为不变；全局计划行下每个账号各自保留，避免互相挤占。
func (r *scheduledTestResultRepository) PruneOldResults(ctx context.Context, planID int64, keepCount int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM scheduled_test_results
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY plan_id, COALESCE(account_id, 0)
					ORDER BY created_at DESC
				) AS rn
				FROM scheduled_test_results
				WHERE plan_id = $1
			) ranked
			WHERE rn > $2
		)
	`, planID, keepCount)
	return err
}

// --- scan helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanPlan(row scannable) (*service.ScheduledTestPlan, error) {
	p := &service.ScheduledTestPlan{}
	var platformModels []byte
	if err := row.Scan(
		&p.ID, &p.AccountID, &p.ModelID, &p.CronExpression, &p.Enabled, &p.MaxResults, &p.AutoRecover,
		&p.LastRunAt, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt, &platformModels,
	); err != nil {
		return nil, err
	}
	if len(platformModels) > 0 {
		models := map[string]string{}
		if err := json.Unmarshal(platformModels, &models); err != nil {
			return nil, err
		}
		p.PlatformModels = models
	}
	return p, nil
}

func scanPlans(rows *sql.Rows) ([]*service.ScheduledTestPlan, error) {
	var plans []*service.ScheduledTestPlan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func scanResult(row scannable) (*service.ScheduledTestResult, error) {
	out := &service.ScheduledTestResult{}
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.AccountID, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return out, nil
}

// scanResultWithGlobal 额外扫描 is_global 标记（按账号聚合查询使用）。
func scanResultWithGlobal(row scannable) (*service.ScheduledTestResult, error) {
	out := &service.ScheduledTestResult{}
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.AccountID, &out.IsGlobal, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return out, nil
}

func scanResults(rows *sql.Rows) ([]*service.ScheduledTestResult, error) {
	var results []*service.ScheduledTestResult
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// platformModelsJSON 序列化平台模型映射；nil 视为空对象。
func platformModelsJSON(models map[string]string) []byte {
	if len(models) == 0 {
		return []byte(`{}`)
	}
	data, err := json.Marshal(models)
	if err != nil {
		return []byte(`{}`)
	}
	return data
}

//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// scheduledTestRepoFixture 在集成库中创建测试所需的账号与计划行，
// 并注册清理函数。数据经 integrationDB 直写（提交），与被测仓储同一连接池。
type scheduledTestRepoFixture struct {
	planRepo  service.ScheduledTestPlanRepository
	resultRep service.ScheduledTestResultRepository
	accountID int64
	planID    int64
}

func newScheduledTestRepoFixture(t *testing.T) *scheduledTestRepoFixture {
	t.Helper()
	ctx := context.Background()

	var accountID int64
	err := integrationDB.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type, status) VALUES ('st-it', 'anthropic', 'apikey', 'active') RETURNING id
	`).Scan(&accountID)
	require.NoError(t, err, "insert test account")

	var planID int64
	err = integrationDB.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at)
		VALUES ($1, 'm', '0 * * * *', true, 50, false, NOW() - INTERVAL '1 minute') RETURNING id
	`, accountID).Scan(&planID)
	require.NoError(t, err, "insert test plan")

	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(),
			`DELETE FROM scheduled_test_plans WHERE id = $1`, planID)
		_, _ = integrationDB.ExecContext(context.Background(),
			`DELETE FROM accounts WHERE id = $1`, accountID)
	})

	return &scheduledTestRepoFixture{
		planRepo:  NewScheduledTestPlanRepository(integrationDB),
		resultRep: NewScheduledTestResultRepository(integrationDB),
		accountID: accountID,
		planID:    planID,
	}
}

func (f *scheduledTestRepoFixture) insertResult(t *testing.T, accountID any, createdOffset string) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `
		INSERT INTO scheduled_test_results (plan_id, account_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at)
		VALUES ($1, $2, 'success', '', '', 10, NOW(), NOW(), NOW() + $3::interval)
	`, f.planID, accountID, createdOffset)
	require.NoError(t, err, "insert test result")
}

// TestScheduledTestClaimForRun_AtomicExclusive 验证认领的原子排他性：
// 同一到期计划只有第一次 ClaimForRun 成功；未到期与禁用行不能被认领。
func TestScheduledTestClaimForRun_AtomicExclusive(t *testing.T) {
	f := newScheduledTestRepoFixture(t)
	ctx := context.Background()

	now := time.Now()
	next := now.Add(time.Hour)

	// 第一次认领到期行：成功
	claimed, err := f.planRepo.ClaimForRun(ctx, f.planID, now, next)
	require.NoError(t, err)
	require.True(t, claimed, "first claim on due plan should succeed")

	// 认领后 next_run_at 已推进到未来：再次认领必须失败（防重复执行）
	claimed, err = f.planRepo.ClaimForRun(ctx, f.planID, time.Now(), next.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, claimed, "second claim before next_run_at must fail")

	// 禁用行即使到期也不能被认领
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE scheduled_test_plans SET enabled = false, next_run_at = NOW() - INTERVAL '1 minute' WHERE id = $1
	`, f.planID)
	require.NoError(t, err)
	claimed, err = f.planRepo.ClaimForRun(ctx, f.planID, time.Now(), next.Add(2*time.Hour))
	require.NoError(t, err)
	require.False(t, claimed, "claim on disabled plan must fail")
}

// TestScheduledTestRepo_GlobalPlanUniqueness 验证迁移 235 的部分唯一索引：
// 数据库层面阻止出现第二条全局计划行。
func TestScheduledTestRepo_GlobalPlanUniqueness(t *testing.T) {
	ctx := context.Background()

	// 全局保留行由迁移插入；再插一条必须违反唯一索引
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, platform_models, next_run_at)
		VALUES (NULL, '', '0 * * * *', false, '{}'::jsonb, NULL)
	`)
	require.Error(t, err, "second global plan row must be rejected by unique index")

	repo := NewScheduledTestPlanRepository(integrationDB)
	global, err := repo.GetGlobal(ctx)
	require.NoError(t, err)
	require.NotNil(t, global)
	require.Nil(t, global.AccountID)
}

// TestScheduledTestRepo_PrunePerAccountUnderGlobalPlan 验证全局计划下
// 修剪按 (plan_id, account_id) 分组：每账号各自保留 keepCount 条，互不挤占。
func TestScheduledTestRepo_PrunePerAccountUnderGlobalPlan(t *testing.T) {
	f := newScheduledTestRepoFixture(t)
	ctx := context.Background()

	// 两个账号各写 4 条全局计划结果
	var account2 int64
	err := integrationDB.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type, status) VALUES ('st-it-2', 'openai', 'apikey', 'active') RETURNING id
	`).Scan(&account2)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account2)
	})

	for i := 0; i < 4; i++ {
		f.insertResult(t, f.accountID, fmtSecondsOffset(i*60))
		f.insertResult(t, account2, fmtSecondsOffset(i*60+30))
	}

	require.NoError(t, f.resultRep.PruneOldResults(ctx, f.planID, 2))

	var count1, count2 int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduled_test_results WHERE plan_id = $1 AND account_id = $2`, f.planID, f.accountID).Scan(&count1))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduled_test_results WHERE plan_id = $1 AND account_id = $2`, f.planID, account2).Scan(&count2))
	require.Equal(t, 2, count1, "per-account prune should keep 2 rows for account 1")
	require.Equal(t, 2, count2, "per-account prune should keep 2 rows for account 2")
}

// TestScheduledTestRepo_ListByAccountMarksGlobal 验证按账号聚合查询
// 正确区分全局计划与按账号计划的结果来源。
func TestScheduledTestRepo_ListByAccountMarksGlobal(t *testing.T) {
	f := newScheduledTestRepoFixture(t)
	ctx := context.Background()

	// 该账号计划的结果 + 全局计划的结果
	f.insertResult(t, f.accountID, "0 seconds")
	globalID, err := f.planRepo.GetGlobal(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO scheduled_test_results (plan_id, account_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at)
		VALUES ($1, $2, 'success', '', '', 10, NOW(), NOW(), NOW())
	`, globalID.ID, f.accountID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(),
			`DELETE FROM scheduled_test_results WHERE plan_id = $1 AND account_id = $2`, globalID.ID, f.accountID)
	})

	results, err := f.resultRep.ListByAccountID(ctx, f.accountID, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)

	byPlan := map[int64]bool{}
	for _, r := range results {
		byPlan[r.PlanID] = r.IsGlobal
	}
	require.False(t, byPlan[f.planID], "per-account plan result should not be marked global")
	require.True(t, byPlan[globalID.ID], "global plan result should be marked global")
}

// TestScheduledTestRepo_AccountDeleteKeepsGlobalResults 验证迁移 235 的
// ON DELETE SET NULL：删除账号后全局测试历史保留、仅解除关联。
func TestScheduledTestRepo_AccountDeleteKeepsGlobalResults(t *testing.T) {
	f := newScheduledTestRepoFixture(t)
	ctx := context.Background()

	globalID, err := f.planRepo.GetGlobal(ctx)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO scheduled_test_results (plan_id, account_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at)
		VALUES ($1, $2, 'success', '', '', 10, NOW(), NOW(), NOW())
	`, globalID.ID, f.accountID)
	require.NoError(t, err)

	// 删除账号（按账号计划行由仓储显式清理，这里模拟外键路径）
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, f.accountID)
	require.NoError(t, err)
	// 防止 cleanup 二次删除报错：账号已删，清理语句幂等

	var accountID sql.NullInt64
	err = integrationDB.QueryRowContext(ctx,
		`SELECT account_id FROM scheduled_test_results WHERE plan_id = $1 LIMIT 1`, globalID.ID).Scan(&accountID)
	require.NoError(t, err, "global result row must survive account deletion")
	require.False(t, accountID.Valid, "account_id should be set to NULL after account deletion")
}

func fmtSecondsOffset(sec int) string {
	d := time.Duration(sec) * time.Second
	return d.String()
}

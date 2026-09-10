-- 235_add_global_scheduled_test.sql
-- 全局定时测试：支持一条不绑定账号的全局计划行，按平台配置测试模型

-- 计划表：account_id 放宽为可空（NULL 即全局计划行）
ALTER TABLE scheduled_test_plans ALTER COLUMN account_id DROP NOT NULL;

-- 全局计划按平台（anthropic/openai/gemini 等）配置测试模型
ALTER TABLE scheduled_test_plans ADD COLUMN IF NOT EXISTS platform_models JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 结果表：冗余 account_id，便于按账号回看全局测试结果。
-- 账号删除时置空而非级联删除：全局测试历史是故障诊断证据，必须保留。
ALTER TABLE scheduled_test_results ADD COLUMN IF NOT EXISTS account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL;

-- 回填历史结果的 account_id（来自按账号计划；这些结果随计划行级联删除，
-- 外键仅影响显式删除账号时未随计划清理的残留场景，同样置空保留）
UPDATE scheduled_test_results r
SET account_id = p.account_id
FROM scheduled_test_plans p
WHERE r.plan_id = p.id
  AND r.account_id IS NULL
  AND p.account_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_str_account_created ON scheduled_test_results(account_id, created_at DESC) WHERE account_id IS NOT NULL;

-- 数据库级唯一保障：全局计划行（account_id IS NULL）至多一条。
-- 部分唯一表达式索引比 WHERE NOT EXISTS 插入更可靠，
-- 防止并发迁移/人工写库产生多条全局计划导致重复调度。
CREATE UNIQUE INDEX IF NOT EXISTS uq_stp_single_global_plan ON scheduled_test_plans ((1)) WHERE account_id IS NULL;

-- 幂等插入全局保留计划行：默认关闭，启用时由服务端重算 next_run_at；
-- 唯一索引兜底并发插入（ON CONFLICT 不支持部分表达式索引，依赖 DO NOTHING + 约束）
INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, platform_models, next_run_at, created_at, updated_at)
SELECT NULL, '', '0 * * * *', false, 50, true, '{}'::jsonb, NULL, NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM scheduled_test_plans WHERE account_id IS NULL);

-- 236_add_global_scheduled_test_pacing.sql
-- 全局定时测试削峰：并发上限与派发间隔可配置，
-- 避免"整点突发全量测试"造成服务器负载/CPU 尖峰。

-- 批次内并发上限（1~20）。ADD COLUMN ... NOT NULL DEFAULT 会为现有行
-- （含全局保留计划行与按账号计划行）统一填充默认值，无需单独回填；
-- 按账号计划行不读取这两列，默认值无行为影响。
ALTER TABLE scheduled_test_plans ADD COLUMN IF NOT EXISTS max_workers INT NOT NULL DEFAULT 3;

-- 派发间隔（秒，0~600）：每隔 N 秒派发下一个账号测试，把整批摊开成时间窗。
-- 0 表示不间隔（近似旧行为的突发模式）。
ALTER TABLE scheduled_test_plans ADD COLUMN IF NOT EXISTS dispatch_interval_seconds INT NOT NULL DEFAULT 5;

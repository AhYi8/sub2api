package service

import (
	"context"
	"errors"
	"strings"
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
			9:       {ID: 9, AccountID: func() *int64 { v := int64(42); return &v }()},
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

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type tempUnschedulablePolicyCacheEntry struct {
	policy    *service.TempUnschedulablePolicy
	expiresAt time.Time
}

type tempUnschedulablePolicyRepository struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]tempUnschedulablePolicyCacheEntry
}

// NewTempUnschedulablePolicyRepository 创建平台级临时不可调度策略仓储。
func NewTempUnschedulablePolicyRepository(_ *dbent.Client, db *sql.DB) service.TempUnschedulablePolicyRepository {
	return &tempUnschedulablePolicyRepository{db: db, cache: make(map[string]tempUnschedulablePolicyCacheEntry)}
}

func (r *tempUnschedulablePolicyRepository) GetTempUnschedulablePolicy(ctx context.Context, platform string) (*service.TempUnschedulablePolicy, error) {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return &service.TempUnschedulablePolicy{}, nil
	}
	now := time.Now()
	r.mu.RLock()
	entry, ok := r.cache[platform]
	r.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return cloneTempUnschedulablePolicy(entry.policy), nil
	}
	if r.db == nil {
		return &service.TempUnschedulablePolicy{Platform: platform}, nil
	}
	var enabled bool
	var raw []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT enabled, rules
		FROM temp_unschedulable_policies
		WHERE platform = $1`, platform).Scan(&enabled, &raw)
	if err != nil {
		if err == sql.ErrNoRows {
			policy := &service.TempUnschedulablePolicy{Platform: platform}
			r.setCache(platform, policy)
			return cloneTempUnschedulablePolicy(policy), nil
		}
		return nil, fmt.Errorf("读取平台临时不可调度策略失败: %w", err)
	}
	var rules []service.TempUnschedulableRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("解析平台临时不可调度规则失败: %w", err)
	}
	policy := &service.TempUnschedulablePolicy{Platform: platform, Enabled: enabled, Rules: rules}
	policy.Rules = normalizePolicyRules(policy.Rules)
	r.setCache(platform, policy)
	return cloneTempUnschedulablePolicy(policy), nil
}

func (r *tempUnschedulablePolicyRepository) SetTempUnschedulablePolicy(ctx context.Context, policy *service.TempUnschedulablePolicy) error {
	if policy == nil {
		return fmt.Errorf("平台策略不能为空")
	}
	policyCopy := cloneTempUnschedulablePolicy(policy)
	policyCopy.Platform = strings.TrimSpace(policyCopy.Platform)
	if policyCopy.Platform == "" {
		return fmt.Errorf("平台不能为空")
	}
	policyCopy.Rules = normalizePolicyRules(policyCopy.Rules)
	raw, err := json.Marshal(policyCopy.Rules)
	if err != nil {
		return fmt.Errorf("序列化平台临时不可调度规则失败: %w", err)
	}
	if r.db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO temp_unschedulable_policies (platform, enabled, rules, updated_at)
		VALUES ($1, $2, $3::jsonb, NOW())
		ON CONFLICT (platform) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			rules = EXCLUDED.rules,
			updated_at = NOW()`, policyCopy.Platform, policyCopy.Enabled, string(raw))
	if err != nil {
		return fmt.Errorf("保存平台临时不可调度策略失败: %w", err)
	}
	r.setCache(policyCopy.Platform, policyCopy)
	return nil
}

func (r *tempUnschedulablePolicyRepository) setCache(platform string, policy *service.TempUnschedulablePolicy) {
	r.mu.Lock()
	r.cache[platform] = tempUnschedulablePolicyCacheEntry{policy: cloneTempUnschedulablePolicy(policy), expiresAt: time.Now().Add(time.Minute)}
	r.mu.Unlock()
}

func cloneTempUnschedulablePolicy(policy *service.TempUnschedulablePolicy) *service.TempUnschedulablePolicy {
	if policy == nil {
		return nil
	}
	clone := *policy
	clone.Rules = append([]service.TempUnschedulableRule(nil), policy.Rules...)
	for i := range clone.Rules {
		clone.Rules[i].Keywords = append([]string(nil), clone.Rules[i].Keywords...)
	}
	return &clone
}

func normalizePolicyRules(rules []service.TempUnschedulableRule) []service.TempUnschedulableRule {
	out := make([]service.TempUnschedulableRule, 0, len(rules))
	for _, rule := range rules {
		if rule.ErrorCode <= 0 || rule.DurationMinutes <= 0 {
			continue
		}
		keywords := make([]string, 0, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			if keyword = strings.TrimSpace(keyword); keyword != "" {
				keywords = append(keywords, keyword)
			}
		}
		if len(keywords) == 0 {
			continue
		}
		rule.Keywords = keywords
		out = append(out, rule)
	}
	return out
}

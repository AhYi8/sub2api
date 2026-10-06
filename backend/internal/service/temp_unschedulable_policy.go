package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type TempUnschedulablePolicy struct {
	Platform string                  `json:"platform"`
	Enabled  bool                    `json:"enabled"`
	Rules    []TempUnschedulableRule `json:"rules"`
}

type TempUnschedulablePolicyRepository interface {
	GetTempUnschedulablePolicy(ctx context.Context, platform string) (*TempUnschedulablePolicy, error)
	SetTempUnschedulablePolicy(ctx context.Context, policy *TempUnschedulablePolicy) error
}

func normalizeTempUnschedulableRules(rules []TempUnschedulableRule) []TempUnschedulableRule {
	out := make([]TempUnschedulableRule, 0, len(rules))
	for _, rule := range rules {
		if rule.ErrorCode <= 0 || rule.DurationMinutes <= 0 || len(rule.Keywords) == 0 {
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

func (p *TempUnschedulablePolicy) normalize() {
	if p == nil {
		return
	}
	p.Platform = strings.TrimSpace(p.Platform)
	p.Rules = normalizeTempUnschedulableRules(p.Rules)
}

func (s *SettingService) GetTempUnschedulablePolicy(ctx context.Context, platform string) (*TempUnschedulablePolicy, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return &TempUnschedulablePolicy{Rules: []TempUnschedulableRule{}}, nil
	}
	if !IsAllowedSchedulingStrategyPlatform(platform) {
		return nil, fmt.Errorf("不支持的平台：%s", platform)
	}
	if s == nil || s.tempPolicyRepo == nil {
		return &TempUnschedulablePolicy{Platform: platform, Rules: []TempUnschedulableRule{}}, nil
	}
	policy, err := s.tempPolicyRepo.GetTempUnschedulablePolicy(ctx, platform)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		policy = &TempUnschedulablePolicy{}
	}
	policy.Platform = platform
	if policy.Rules == nil {
		policy.Rules = []TempUnschedulableRule{}
	}
	return policy, nil
}

func (s *SettingService) SetTempUnschedulablePolicy(ctx context.Context, platform string, policy TempUnschedulablePolicy) error {
	policy.Platform = strings.ToLower(strings.TrimSpace(platform))
	if policy.Platform == "" {
		return fmt.Errorf("平台不能为空")
	}
	if !IsAllowedSchedulingStrategyPlatform(policy.Platform) {
		return fmt.Errorf("不支持的平台：%s", policy.Platform)
	}
	for _, rule := range policy.Rules {
		if rule.ErrorCode < 100 || rule.ErrorCode > 599 {
			return fmt.Errorf("规则 HTTP 状态码必须在 100-599 之间")
		}
		if rule.DurationMinutes <= 0 || rule.DurationMinutes > 7*24*60 {
			return fmt.Errorf("规则持续时间必须在 1-10080 分钟之间")
		}
		if len(rule.Keywords) == 0 {
			return fmt.Errorf("规则至少需要一个关键词")
		}
		for _, keyword := range rule.Keywords {
			if strings.TrimSpace(keyword) == "" || len([]rune(keyword)) > 256 {
				return fmt.Errorf("规则关键词不能为空且不能超过 256 个字符")
			}
		}
	}
	policy.normalize()
	if s == nil || s.tempPolicyRepo == nil {
		return fmt.Errorf("平台策略仓储未初始化")
	}
	return s.tempPolicyRepo.SetTempUnschedulablePolicy(ctx, &policy)
}

// EffectiveTempUnschedulableRules 返回账号最终生效的规则。旧账号没有 mode 时按旧字段兼容。
func (s *SettingService) EffectiveTempUnschedulableRules(ctx context.Context, account *Account) ([]TempUnschedulableRule, bool) {
	if s == nil {
		return EffectiveTempUnschedulableRules(ctx, nil, account)
	}
	return EffectiveTempUnschedulableRules(ctx, s.tempPolicyRepo, account)
}

func EffectiveTempUnschedulableRules(ctx context.Context, repo TempUnschedulablePolicyRepository, account *Account) ([]TempUnschedulableRule, bool) {
	if account == nil {
		return nil, false
	}
	mode := ""
	if account.Credentials != nil {
		mode, _ = account.Credentials["temp_unschedulable_mode"].(string)
		mode = strings.ToLower(strings.TrimSpace(mode))
	}
	switch mode {
	case "disabled":
		return nil, false
	case "override":
		// 新三态字段是规范来源；旧 enabled 字段仅用于兼容旧账号。
		// 清理旧字段后，override 仍必须继续生效。
		rules := account.GetTempUnschedulableRules()
		return rules, len(rules) > 0
	case "inherit":
	default:
		if account.Credentials != nil {
			if enabled, ok := account.Credentials["temp_unschedulable_enabled"].(bool); ok {
				if !enabled {
					return nil, false
				}
				rules := account.GetTempUnschedulableRules()
				return rules, len(rules) > 0
			}
		}
	}
	if repo == nil {
		return nil, false
	}
	policy, err := repo.GetTempUnschedulablePolicy(ctx, strings.ToLower(strings.TrimSpace(account.Platform)))
	if err != nil {
		slog.Warn("读取平台临时不可调度策略失败，暂不应用策略", "platform", account.Platform, "error", err)
		return nil, false
	}
	if policy == nil || !policy.Enabled {
		return nil, false
	}
	return policy.Rules, len(policy.Rules) > 0
}

// ApplyTempUnschedulableMode 应用账号三态配置，并保留无关凭据。
func ApplyTempUnschedulableMode(credentials map[string]any, mode string, creating bool) map[string]any {
	if credentials == nil {
		credentials = make(map[string]any)
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" && creating {
		mode, _ = credentials["temp_unschedulable_mode"].(string)
		mode = strings.ToLower(strings.TrimSpace(mode))
		if mode != "inherit" && mode != "override" && mode != "disabled" {
			// 导入或旧客户端仍可能只提交 legacy 字段，必须保留其语义。
			if enabled, ok := credentials["temp_unschedulable_enabled"].(bool); ok {
				if enabled {
					mode = "override"
				} else {
					mode = "disabled"
				}
			} else {
				mode = "disabled"
			}
		}
	}
	if mode != "inherit" && mode != "override" && mode != "disabled" {
		return credentials
	}
	credentials["temp_unschedulable_mode"] = mode
	if mode == "override" {
		credentials["temp_unschedulable_enabled"] = true
	} else {
		delete(credentials, "temp_unschedulable_enabled")
		delete(credentials, "temp_unschedulable_rules")
	}
	return credentials
}

package service

import (
	"fmt"
	"strings"
)

// OpenAIPersonaAdmissionPolicy 将并发、RPM/TPM、子代理和 WS 约束收敛到
// Persona 配置层。字段为 0 时继承旧的全局配置，避免历史 JSON 失效。
type OpenAIPersonaAdmissionPolicy struct {
	MaxConcurrency          int   `json:"max_concurrency,omitempty"`
	MaxActiveClientSessions int   `json:"max_active_client_sessions,omitempty"`
	RequestsPerMinute       int   `json:"requests_per_minute,omitempty"`
	TokensPerMinute         int64 `json:"tokens_per_minute,omitempty"`
	MaxSubagents            int   `json:"max_subagents,omitempty"`
	SubagentDepth           int   `json:"subagent_depth,omitempty"`
	MaxActiveWebSockets     int   `json:"max_active_websockets,omitempty"`
	MaxQueueDepthPerAccount int   `json:"max_queue_depth_per_account,omitempty"`
}

const (
	maxPersonaPolicyConcurrency = 100000
	maxPersonaPolicySessions    = 10000
	maxPersonaPolicyRPM         = 1000000
	maxPersonaPolicyTPM         = 1000000000
	maxPersonaPolicySubagents   = 100000
	maxPersonaPolicyDepth       = 64
	maxPersonaPolicyWS          = 10000
	maxPersonaPolicyQueue       = 100000
)

func (p OpenAIPersonaAdmissionPolicy) validate() error {
	if p.MaxConcurrency < 0 || p.MaxConcurrency > maxPersonaPolicyConcurrency {
		return fmt.Errorf("max_concurrency must be between 0 and %d", maxPersonaPolicyConcurrency)
	}
	if p.MaxActiveClientSessions < 0 || p.MaxActiveClientSessions > maxPersonaPolicySessions {
		return fmt.Errorf("max_active_client_sessions must be between 0 and %d", maxPersonaPolicySessions)
	}
	if p.RequestsPerMinute < 0 || p.RequestsPerMinute > maxPersonaPolicyRPM {
		return fmt.Errorf("requests_per_minute must be between 0 and %d", maxPersonaPolicyRPM)
	}
	if p.TokensPerMinute < 0 || p.TokensPerMinute > maxPersonaPolicyTPM {
		return fmt.Errorf("tokens_per_minute must be between 0 and %d", maxPersonaPolicyTPM)
	}
	if p.MaxSubagents < 0 || p.MaxSubagents > maxPersonaPolicySubagents {
		return fmt.Errorf("max_subagents must be between 0 and %d", maxPersonaPolicySubagents)
	}
	if p.SubagentDepth < 0 || p.SubagentDepth > maxPersonaPolicyDepth {
		return fmt.Errorf("subagent_depth must be between 0 and %d", maxPersonaPolicyDepth)
	}
	if p.MaxActiveWebSockets < 0 || p.MaxActiveWebSockets > maxPersonaPolicyWS {
		return fmt.Errorf("max_active_websockets must be between 0 and %d", maxPersonaPolicyWS)
	}
	if p.MaxQueueDepthPerAccount < 0 || p.MaxQueueDepthPerAccount > maxPersonaPolicyQueue {
		return fmt.Errorf("max_queue_depth_per_account must be between 0 and %d", maxPersonaPolicyQueue)
	}
	return nil
}

// EffectiveOpenAIPersonaPolicy resolves one Persona against the legacy global
// settings. The returned policy is a value copy and safe to attach to a ticket.
func (cfg OpenAIAccountAdmissionConfig) EffectiveOpenAIPersonaPolicy(persona SessionPersonaID) OpenAIPersonaAdmissionPolicy {
	policy := OpenAIPersonaAdmissionPolicy{
		MaxConcurrency:          cfg.MaxConcurrency,
		MaxActiveClientSessions: cfg.MaxActiveClientSessions,
		RequestsPerMinute:       cfg.RequestsPerMinute,
		TokensPerMinute:         cfg.TokensPerMinute,
		MaxSubagents:            cfg.MaxSubagents,
		SubagentDepth:           cfg.SubagentDepth,
		MaxActiveWebSockets:     cfg.MaxActiveWebSockets,
		MaxQueueDepthPerAccount: cfg.MaxQueueDepthPerAccount,
	}
	id := strings.ToLower(strings.TrimSpace(string(persona)))
	if id == "" || cfg.PersonaPolicies == nil {
		return policy
	}
	var override OpenAIPersonaAdmissionPolicy
	var found bool
	for rawID, candidate := range cfg.PersonaPolicies {
		if strings.ToLower(strings.TrimSpace(rawID)) == id {
			override, found = candidate, true
			break
		}
	}
	if found {
		if override.MaxConcurrency > 0 {
			policy.MaxConcurrency = override.MaxConcurrency
		}
		if override.MaxActiveClientSessions > 0 {
			policy.MaxActiveClientSessions = override.MaxActiveClientSessions
		}
		if override.RequestsPerMinute > 0 {
			policy.RequestsPerMinute = override.RequestsPerMinute
		}
		if override.TokensPerMinute > 0 {
			policy.TokensPerMinute = override.TokensPerMinute
		}
		if override.MaxSubagents > 0 {
			policy.MaxSubagents = override.MaxSubagents
		}
		if override.SubagentDepth > 0 {
			policy.SubagentDepth = override.SubagentDepth
		}
		if override.MaxActiveWebSockets > 0 {
			policy.MaxActiveWebSockets = override.MaxActiveWebSockets
		}
		if override.MaxQueueDepthPerAccount > 0 {
			policy.MaxQueueDepthPerAccount = override.MaxQueueDepthPerAccount
		}
	}
	return policy
}

// EffectiveOpenAIPersonaPolicyForAccount 把 Persona 的 0 值继承解析到
// 具体账号与全站 WS 配置。调用方拿到的是可直接用于运行时准入的快照。
func (cfg OpenAIAccountAdmissionConfig) EffectiveOpenAIPersonaPolicyForAccount(
	account *Account,
	persona SessionPersonaID,
	globalMaxActiveWebSockets int,
) OpenAIPersonaAdmissionPolicy {
	policy := cfg.EffectiveOpenAIPersonaPolicy(persona)
	if policy.MaxConcurrency <= 0 {
		policy.MaxConcurrency = legacyOpenAIAccountConcurrency(account)
	}
	if policy.MaxSubagents <= 0 && persona == SessionPersonaCodexCLIStrict && account != nil {
		policy.MaxSubagents = account.GetCodexSubagentMaxInflightPerSession()
	}
	if policy.MaxActiveWebSockets <= 0 && globalMaxActiveWebSockets > 0 {
		policy.MaxActiveWebSockets = globalMaxActiveWebSockets
	}
	return policy
}

// EffectiveOpenAIAccountPersonaCapacity 汇总 active 且授权就绪的动态 Persona。
func EffectiveOpenAIAccountPersonaCapacity(account *Account, personas []OpenAIAccountPersona, cfg OpenAIAccountAdmissionConfig) int {
	if account == nil || !account.IsOpenAI() || !account.IsOpenAIOAuth() {
		return legacyOpenAIAccountConcurrency(account)
	}
	capacity := 0
	for _, persona := range personas {
		if !persona.AcceptsNewRoot() {
			continue
		}
		capacity += cfg.EffectiveOpenAIPersonaPolicyForAccount(account, persona.ProfileID, 0).MaxConcurrency
	}
	return capacity
}

// EffectiveOpenAIAccountAdmissionCapacity 仅保留非动态调用方的兼容语义。
// 动态 OpenAI OAuth 管理与号池展示必须调用上面的数据库 Persona 聚合器。
func EffectiveOpenAIAccountAdmissionCapacity(account *Account, cfg OpenAIAccountAdmissionConfig) int {
	if account != nil && account.IsOpenAI() && account.IsOpenAIOAuth() {
		// 动态 OAuth 容量只能由数据库 Persona 聚合器提供。
		return 0
	}
	return legacyOpenAIAccountConcurrency(account)
}

func legacyOpenAIAccountConcurrency(account *Account) int {
	if account != nil && account.Concurrency > 0 {
		return account.Concurrency
	}
	return 1
}

// ForPersona 返回带 Persona 限制的 admission 配置快照。旧字段仍保留，
// 以便现有队列脚本和管理接口在迁移期间继续工作。
func (cfg OpenAIAccountAdmissionConfig) ForPersona(persona SessionPersonaID) OpenAIAccountAdmissionConfig {
	policy := cfg.EffectiveOpenAIPersonaPolicy(persona)
	if policy.MaxActiveClientSessions > 0 {
		cfg.MaxActiveClientSessions = policy.MaxActiveClientSessions
	}
	if policy.RequestsPerMinute > 0 {
		cfg.RequestsPerMinute = policy.RequestsPerMinute
	}
	if policy.TokensPerMinute > 0 {
		cfg.TokensPerMinute = policy.TokensPerMinute
	}
	if policy.MaxQueueDepthPerAccount > 0 {
		cfg.MaxQueueDepthPerAccount = policy.MaxQueueDepthPerAccount
	}
	return cfg
}

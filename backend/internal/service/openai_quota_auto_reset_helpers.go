package service

// readOpenAIQuotaUsedPercent 读取自动重置流程所需的窗口使用率百分比。
// 该辅助函数只服务于上游自动重置能力，不参与本地账号调度决策。
func readOpenAIQuotaUsedPercent(extra map[string]any, window string) float64 {
	if len(extra) == 0 {
		return 0
	}
	value, ok := resolveAccountExtraNumber(extra, "codex_"+window+"_used_percent")
	if !ok {
		return 0
	}
	return value
}

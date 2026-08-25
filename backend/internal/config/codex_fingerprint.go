package config

// Codex Session epoch 生命周期的默认值与安全边界。
// 网关设置与静态配置共用这一组约束，避免不同入口接受不一致的值。
const (
	CodexFingerprintMinSessionAgeHoursDefault  = 72
	CodexFingerprintMaxSessionAgeHoursDefault  = 168
	CodexFingerprintRotationJitterHoursDefault = 24
	CodexFingerprintIdleGateMinutesDefault     = 120
	CodexFingerprintOldEpochGraceHoursDefault  = 48

	CodexFingerprintSessionAgeHoursMax = 8760
	CodexFingerprintRotationJitterMax  = 8760
	CodexFingerprintIdleGateMinutesMax = 10080
	CodexFingerprintOldEpochGraceMax   = 8760
)

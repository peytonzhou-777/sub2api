export const openAIUserAffinityAccounts = {
  title: 'User Affinity Residents', short: 'Residents', user: 'User', lastActive: 'Last active',
  residenceExpiry: 'Residence expiry', migrationHistory: 'Migration history', resetReason: 'Reset reason',
  reset: 'Reset placement', inherit: 'Inherit global', maxContactUsers: 'Maximum contacted users',
  cooldownSeconds: 'New resident cooldown (seconds)', failureThreshold: 'Migration failure threshold', failureWindow: 'Failure window (seconds)'
}

export const openAIUserAffinitySettings = {
  title: 'OpenAI User Affinity Scheduling',
  state: 'Effective state',
  states: { disabled: 'Disabled', shadow: 'Shadow', enforce: 'Enforced' },
  mode: 'Mode',
  bestFitStrategy: 'Best Fit primary window',
  touchSuccessMode: 'Touch success point',
  maxContactUsers: 'Default maximum contacted users',
  cooldownSeconds: 'New resident cooldown (seconds)',
  failureThreshold: 'Migration failure threshold',
  failureWindow: 'Failure window (seconds)',
  stabilitySeconds: 'Migration stability (seconds)',
  jitterMin: 'Minimum follower jitter (ms)',
  jitterMax: 'Maximum follower jitter (ms)',
  demandQuantile: 'Cold-start demand quantile',
  reserve5h: '5h quota reserve ratio',
  reserve7d: '7d quota reserve ratio',
  closeTolerance: 'Best Fit close tolerance',
  reentryOvercommit: 'Allow resident reentry overcommit',
  resetExcludeSource: 'Exclude source account after reset',
  changeReason: 'Change reason'
}

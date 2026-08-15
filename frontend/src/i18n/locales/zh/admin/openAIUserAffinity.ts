export const openAIUserAffinityAccounts = {
  title: '用户粘性归属', short: '居民', user: '用户', lastActive: '最后活跃',
  residenceExpiry: '居住到期', migrationHistory: '搬迁记录', resetReason: '重置原因',
  reset: '重置归属', inherit: '继承全局', maxContactUsers: '最大触达用户数',
  cooldownSeconds: '新居民冷却（秒）', failureThreshold: '搬迁失败阈值', failureWindow: '失败窗口（秒）'
}

export const openAIUserAffinitySettings = {
  title: 'OpenAI 用户粘性调度',
  state: '生效状态',
  states: { disabled: '已关闭', shadow: '影子模式', enforce: '强制执行' },
  mode: '运行模式',
  bestFitStrategy: 'Best Fit 主窗口',
  touchSuccessMode: '触达成功口径',
  maxContactUsers: '默认最大触达用户数',
  cooldownSeconds: '新居民冷却（秒）',
  failureThreshold: '搬迁失败阈值',
  failureWindow: '失败窗口（秒）',
  stabilitySeconds: '搬迁稳定期（秒）',
  jitterMin: '错峰最小间隔（毫秒）',
  jitterMax: '错峰最大间隔（毫秒）',
  demandQuantile: '冷启动需求分位数',
  reserve5h: '5h 额度保留比例',
  reserve7d: '7d 额度保留比例',
  closeTolerance: 'Best Fit 接近容差',
  reentryOvercommit: '居民回流允许短暂超配',
  resetExcludeSource: '手动重置后排除原账号',
  changeReason: '变更原因'
}

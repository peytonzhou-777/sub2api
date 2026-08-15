export const openAIUserAffinityAccounts = {
  title: 'User Affinity Residents', short: 'Residents', user: 'User', lastActive: 'Last active',
  residenceExpiry: 'Residence expiry', migrationHistory: 'Migration history', resetReason: 'Reset reason',
  reset: 'Reset placement', inherit: 'Inherit global', maxContactUsers: 'Maximum contacted users',
  cooldownSeconds: 'New resident cooldown (seconds)', failureThreshold: 'Migration failure threshold', failureWindow: 'Failure window (seconds)'
}

export const openAIUserAffinitySettings = {
  title: 'OpenAI User Affinity Scheduling',
  description: 'Keep each user on the same OpenAI upstream account when possible, while controlling new-resident placement, contact capacity, and migration.',
  state: 'Effective state',
  states: { disabled: 'Disabled', shadow: 'Shadow', enforce: 'Enforced' },
  mode: 'Mode',
  modeHint: 'Enforce changes account selection. Shadow only evaluates and records decisions without changing the existing scheduler result.',
  modes: { enforce: 'Enforce', shadow: 'Shadow' },
  bestFitStrategy: 'Best Fit primary window',
  bestFitStrategyHint: 'Choose the quota window compared first for new-resident placement; near-ties also consider the other window and contacted-user count.',
  bestFitStrategies: {
    sevenDayThenFiveHour: '7-day quota first, 5-hour quota second',
    fiveHourThenSevenDay: '5-hour quota first, 7-day quota second'
  },
  touchSuccessMode: 'Touch success point',
  touchSuccessModeHint: 'Choose when an upstream call counts as successful and refreshes the user\'s 7-day contact TTL.',
  touchSuccessModes: {
    upstreamAccepted: 'Upstream accepted the request',
    responseCompleted: 'Response completed'
  },
  maxContactUsers: 'Default maximum contacted users',
  maxContactUsersHint: 'Maximum unique users with an active 7-day contact TTL on an account; new accounts default to 10.',
  cooldownSeconds: 'New resident cooldown (seconds)',
  cooldownSecondsHint: 'After accepting a new resident, the account cannot accept another new or migrating resident for this period.',
  failureThreshold: 'Migration failure threshold',
  failureThresholdHint: 'Allow persistent migration only after repeated client retries for the same user reach this count within the failure window.',
  failureWindow: 'Failure window (seconds)',
  failureWindowHint: 'Window for counting repeated 5-hour exhaustion, RPM, and concurrency-capacity failures.',
  stabilitySeconds: 'Migration stability (seconds)',
  stabilitySecondsHint: 'Avoid another persistent migration for this period after a user moves successfully.',
  jitterMin: 'Minimum follower jitter (ms)',
  jitterMinHint: 'Minimum randomized delay between adjacent FIFO follower requests after the same user\'s leader succeeds.',
  jitterMax: 'Maximum follower jitter (ms)',
  jitterMaxHint: 'Maximum randomized delay between adjacent FIFO follower requests after the same user\'s leader succeeds.',
  demandQuantile: 'Cold-start demand quantile',
  demandQuantileHint: 'Historical usage quantile used to estimate a new resident\'s 5-hour and 7-day quota demand during Best Fit.',
  reserve5h: '5h quota reserve ratio',
  reserve5hHint: 'Stop accepting new residents when remaining 5-hour quota enters this reserve, while continuing to serve existing residents.',
  reserve7d: '7d quota reserve ratio',
  reserve7dHint: 'Stop accepting new residents when remaining 7-day quota enters this reserve, while continuing to serve existing residents.',
  closeTolerance: 'Best Fit close tolerance',
  closeToleranceHint: 'When post-placement quota headroom differs by no more than this ratio, prefer the account with fewer currently contacted users.',
  reentryOvercommit: 'Allow resident reentry overcommit',
  reentryOvercommitHint: 'Allow returning residents to temporarily exceed the contact limit; block new residents while overcommitted.',
  resetExcludeSource: 'Exclude source account after reset',
  resetExcludeSourceHint: 'After an admin resets a residence, do not immediately choose the previous account during the next Best Fit.'
}

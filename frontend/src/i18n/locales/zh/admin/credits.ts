export default {
  credits: {
      title: '额度管理', description: '管理用户永久余额和限时额度', search: '搜索邮箱、用户名或用户 ID', user: '用户', permanent: '永久余额', limited: '限时额度', total: '总额度', items: '份', manage: '管理额度', empty: '暂无用户', frozen: '冻结金额', add: '增加', subtract: '扣减', balanceHistory: '永久余额流水', noHistory: '暂无资金变动记录', limitedDetails: '限时额度详情', activeOnly: '生效中', allRecords: '全部记录', createLimited: '新增限时额度', limitedItem: '限时额度 #{id}', adjustUsed: '调整已用额度', adjustLimit: '调整上限额度', adjustExpiry: '调整有效期', reset: '重置', revoke: '作废', ledger: '查看流水', creditUnit: '额度', dayUnit: '天', amount: '金额', days: '有效天数', notes: '操作原因（可选）', balancePreview: '永久余额：${before} → ${after}', createPreview: '将发放 ${amount}，有效期 {days} 天', limitPreview: '上限额度：${before} → ${after}', usedPreview: '已用额度：${before} → ${after}', expiryPreview: '操作后到期时间：{date}', resetPreview: '已用额度将重置为 0，到期时间保持不变。', resetWithExpiryPreview: '已用额度将重置为 0，到期时间将从现在起重置为 {days} 天。', revokePreview: '作废后剩余可用额度将立即失效。',
      balanceConflict: { title: '用户资料已发生变化', message: '该用户资料已发生变化，页面已刷新最新数据。是否使用最新数据重试本次额度调整？', retry: '确认重试', retryExhausted: '用户资料再次发生变化，请确认最新数据后重新操作' },
      actions: { none: '', 'balance-add': '增加永久余额', 'balance-subtract': '扣减永久余额', 'limited-create': '新增限时额度', 'limited-used': '调整已用额度', 'limited-limit': '调整上限额度', 'limited-expiry': '调整限时额度有效期', 'limited-reset': '重置限时额度', 'limited-revoke': '作废限时额度' },
      tabs: { users: '用户额度', rechargeActivities: '充值活动', recurringGrants: '赠额任务', resetRebates: '重置返利' },
      sources: { promo_code: '优惠码兑换', redeem_code: '兑换码', default_user_setting: '默认设置', recharge_bonus: '充值赠送', admin_manual: '管理员发放', reset_rebate: '重置返利', recurring_grant: '赠额任务' },
      statuses: { active: '生效中', depleted: '已耗尽', expired: '已过期', revoked: '已作废' },
      events: { grant: '额度发放', consume: '请求扣费', reserve: '额度冻结', capture: '冻结结算', release: '冻结释放', admin_increase_limit: '管理员增加上限', admin_decrease_limit: '管理员扣减上限', admin_increase_used: '管理员增加已用', admin_decrease_used: '管理员扣减已用', admin_extend_expiry: '管理员延长有效期', admin_reduce_expiry: '管理员缩短有效期', admin_reset: '管理员重置', admin_revoke: '管理员作废' },
      balanceEvents: { balance: '兑换码或充值', admin_balance: '管理员调整', affiliate_balance: '返利转入' },
      resetRebates: { title: '重置返利' },
      recurringGrants: { title: '赠额任务' }
  }
}

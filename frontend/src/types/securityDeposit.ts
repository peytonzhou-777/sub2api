import type { CreateOrderResult } from './payment'

export type SecurityDepositBucketType = 'paid' | 'admin_grant'

export interface SecurityDepositLot {
  id: number
  bucket_type: SecurityDepositBucketType
  source_type: string
  payment_order_id?: number
  original_cents: number
  remaining_cents: number
  refund_reserved_cents: number
  forfeited_cents: number
  refunded_cents: number
  admin_deducted_cents: number
  revoked_cents: number
  currency: string
  locked_until: string | null
  refund_policy: string
  status: string
  created_at: string
  refund_eligible: boolean
  self_refund_eligible: boolean
  refund_block_reason?: string
  admin_action_required: boolean
  provider_refund_enabled: boolean
  provider_user_refund_enabled: boolean
}

export interface SecurityDepositAccount {
  currency: string
  paid_balance_cents: number
  admin_grant_balance_cents: number
  total_balance_cents: number
  effective_balance_cents: number
  timed_locked_cents: number
  permanent_locked_cents: number
  refundable_cents: number
  paid_refund_reserved_cents: number
  cyber_strike_count: number
  risk_multiplier: number
  max_risk_multiplier: number
  next_unlock_at: string | null
  enforcement_enabled: boolean
  self_refund_enabled: boolean
  bonus?: SecurityDepositBonusEstimate
  lots: SecurityDepositLot[]
}

export interface SecurityDepositBonusEstimate {
  enabled: boolean
  qualified: boolean
  reason: 'eligible' | 'enforcement_disabled' | 'daily_amount_disabled' | 'threshold_not_met' | 'cap_reached'
  daily_amount: number
  cap_ratio: number
  current_amount: number
  cap_amount: number
  estimated_grant_amount: number
  next_grant_at: string
  expires_at?: string
  qualifying_group_id?: number
  qualifying_group_name?: string
  required_cents: number
}

export interface SecurityDepositEligibility {
  group_id: number
  group_name: string
  currency: string
  base_required_cents: number
  risk_multiplier: number
  required_cents: number
  effective_balance_cents: number
  shortfall_cents: number
  eligible: boolean
  agreement_required: boolean
  policy_version: string
  content_hash: string
  quote_hash: string
}

export interface SecurityDepositAgreement {
  version: string
  content_hash: string
  content_zh: string
  content_en: string
  freeze_hours: number
}

export interface CreateSecurityDepositOrderRequest {
  group_id: number
  agreement_version: string
  agreement_hash: string
  quote_hash: string
  accepted: true
  payment_type: string
  openid?: string
  wechat_resume_token?: string
  return_url?: string
  payment_source?: string
  is_mobile?: boolean
}

export interface CreateSecurityDepositOrderResult {
  satisfied: boolean
  eligibility: SecurityDepositEligibility
  payment?: CreateOrderResult
}

export interface AdminSecurityDepositUserSummary {
  user_id: number
  email: string
  username: string
  status: string
  paid_balance_cents: number
  admin_grant_balance_cents: number
  total_balance_cents: number
  effective_balance_cents: number
  timed_locked_cents: number
  permanent_locked_cents: number
  refundable_cents: number
  paid_refund_reserved_cents: number
  risk_multiplier: number
  cyber_strike_count: number
  last_violation_at: string | null
}

export interface SecurityDepositLedgerEntry {
  id: number
  lot_id: number
  bucket_type: SecurityDepositBucketType
  entry_type: string
  delta_cents: number
  reserved_delta_cents: number
  bucket_balance_after_cents: number
  bucket_reserved_after_cents: number
  reason?: string | null
  created_at: string
}

export type SecurityDepositRefundMode = 'automatic_original_channel' | 'manual_external'
export type SecurityDepositRefundState = 'reserved' | 'submitting' | 'pending' | 'manual_review' | 'succeeded' | 'failed_released' | 'canceled'

export interface SecurityDepositRefund {
  id: number
  refund_id: string
  lot_id: number
  principal_cents: number
  mode: SecurityDepositRefundMode
  state: SecurityDepositRefundState
  reason?: string | null
  created_at: string
  completed_at?: string | null
}

export interface SecurityDepositRefundImpact {
  api_key_id: number
  api_key_name: string
  group_id: number
  group_name: string
  required_cents: number
  balance_after_cents: number
}

export interface SecurityDepositRefundPreview {
  lot_id: number
  principal_cents: number
  gateway_amount: string
  gateway_currency: string
  affected_api_keys: SecurityDepositRefundImpact[]
}

export interface AdminSecurityDepositUserDetail {
  user: AdminSecurityDepositUserSummary
  account: SecurityDepositAccount
  ledger: SecurityDepositLedgerEntry[]
  refunds: SecurityDepositRefund[]
  violations: Array<Record<string, unknown>>
}

export type AdminSecurityDepositCreditType = 'admin_add' | 'compensation'

export interface AdminSecurityDepositMutationResult {
  action_id: number
  action_type: string
  user_id: number
  lot_id?: number
  amount_cents: number
  admin_grant_balance_after_cents: number
  disabled_key_ids: number[]
  already_processed: boolean
}

export interface AdminSecurityDepositUnlockResult {
  action_id: number
  user_id: number
  api_key_id: number
  status: 'disabled'
  already_processed: boolean
}

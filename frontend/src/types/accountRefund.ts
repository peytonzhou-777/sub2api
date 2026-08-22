import type { AccountRefundActor, AccountRefundQuote, AccountRefundReconciliation, AccountRefundRecord } from './payment'

export type AccountRefundAction =
  | 'start'
  | 'advance'
  | 'confirm'
  | 'continue'
  | 'recalculate'
  | 'reconcile'
  | 'finalize'
  | 'cancel'
  | 'restore-access'

export interface AdminAccountRefundSummary {
  refundable_totals: Record<string, number>
  automatic_totals: Record<string, number>
  manual_external_totals: Record<string, number>
  refundable_users: number
  automatic_users: number
  processing_users: number
  manual_review_users: number
  calculated_at: string
}

export interface AdminAccountRefundListItem {
  user_id: number
  username: string
  email: string
  user_status: string
  permanent_balance: number
  recharge_bonus_balance: number
  other_limited_to_clear: number
  refund_totals: Record<string, number>
  calculation_status: 'verified' | 'manual_review' | 'none'
  self_service_eligible: boolean
  admin_execution_mode: 'automatic' | 'manual_external' | 'blocked'
  review_reason_code?: string
  flow_state: string
  state_revision: number
  available_actions: AccountRefundAction[]
  updated_at: string
}

export interface AdminAccountRefundTimelineEvent {
  state_revision: number
  state: string
  message?: string
  review_reason_code?: string
  failure_stage?: string
  actor?: AccountRefundActor
  reconciliation?: AccountRefundReconciliation
  created_at: string
}

export interface AdminAccountRefundDetail {
  item: AdminAccountRefundListItem
  quote?: AccountRefundQuote
  record?: AccountRefundRecord
  timeline: AdminAccountRefundTimelineEvent[]
}

export interface AdminAccountRefundListParams {
  page?: number
  page_size?: number
  tab?: 'refundable' | 'processing' | 'manual_review' | 'completed' | 'all'
  status?: string
  currency?: string
  keyword?: string
  sort_by?: 'updated_at' | 'email' | 'refund_amount'
  sort_order?: 'asc' | 'desc'
}

export interface AdminAccountRefundActionInput {
  expected_state_revision: number
  quote_hash?: string
}

export interface AdminAccountRefundReconcileInput {
  order_id: number
  outcome: 'succeeded' | 'failed'
  external_refund_id?: string
  verified_at: string
  evidence: string
  note: string
  expected_state_revision: number
}

package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SecurityDepositLedger 是按资金桶记录的不可变保证金流水。
type SecurityDepositLedger struct {
	ent.Schema
}

func (SecurityDepositLedger) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_ledger"}}
}

func (SecurityDepositLedger) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("lot_id"),
		field.Enum("bucket_type").Values("paid", "admin_grant"),
		field.Enum("entry_type").Values("payment_credit", "admin_add", "compensation", "forfeit", "refund_reserve", "refund_release", "refund_success", "admin_deduct", "admin_revoke"),
		field.Int64("delta_cents").Default(0),
		field.Int64("reserved_delta_cents").Default(0),
		field.Int64("bucket_balance_after_cents").Min(0),
		field.Int64("bucket_reserved_after_cents").Min(0),
		field.Int64("group_id").Optional().Nillable(),
		field.Int64("api_key_id").Optional().Nillable(),
		field.Int64("violation_id").Optional().Nillable(),
		field.Int64("refund_id").Optional().Nillable(),
		field.Int64("payment_order_id").Optional().Nillable(),
		field.Int64("operator_id").Optional().Nillable(),
		field.String("reason").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("idempotency_key").MaxLen(191).Unique(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SecurityDepositLedger) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("lot_id", "created_at"),
		index.Fields("violation_id"),
		index.Fields("refund_id"),
	}
}

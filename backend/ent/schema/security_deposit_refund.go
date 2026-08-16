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

// SecurityDepositRefund 保存自动原路退款与人工外部退款状态。
type SecurityDepositRefund struct {
	ent.Schema
}

func (SecurityDepositRefund) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_refunds"}}
}

func (SecurityDepositRefund) Fields() []ent.Field {
	return []ent.Field{
		field.String("refund_id").MaxLen(64).Unique(),
		field.Int64("user_id"),
		field.Int64("lot_id"),
		field.Int64("payment_order_id"),
		field.Int64("principal_cents").Min(1),
		field.String("gateway_amount").MaxLen(64),
		field.String("gateway_currency").MaxLen(3).Default("CNY"),
		field.Enum("mode").Values("automatic_original_channel", "manual_external"),
		field.String("state").MaxLen(32),
		field.Int64("requested_by").Optional().Nillable(),
		field.String("reason").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("quote_hash").MaxLen(128),
		field.String("idempotency_key").MaxLen(191).Unique(),
		field.String("provider_request_id").Optional().Nillable().MaxLen(191),
		field.JSON("provider_response_snapshot", map[string]any{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("external_refund_id").Optional().Nillable().MaxLen(191),
		field.Time("external_refunded_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.JSON("external_evidence", map[string]any{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("submitted_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SecurityDepositRefund) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("lot_id", "state"),
		index.Fields("payment_order_id"),
		index.Fields("external_refund_id").Unique().Annotations(entsql.IndexWhere("external_refund_id IS NOT NULL")),
	}
}

package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SecurityDepositLot 保存每笔保证金来源及其剩余金额。
type SecurityDepositLot struct {
	ent.Schema
}

func (SecurityDepositLot) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_lots"}}
}

func (SecurityDepositLot) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (SecurityDepositLot) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Enum("bucket_type").Values("paid", "admin_grant"),
		field.Enum("source_type").Values("payment", "admin", "compensation"),
		field.Int64("payment_order_id").Optional().Nillable(),
		field.Int64("original_cents").Min(1),
		field.Int64("remaining_cents").Min(0),
		field.Int64("refund_reserved_cents").Default(0).Min(0),
		field.Int64("forfeited_cents").Default(0).Min(0),
		field.Int64("refunded_cents").Default(0).Min(0),
		field.Int64("admin_deducted_cents").Default(0).Min(0),
		field.Int64("revoked_cents").Default(0).Min(0),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Time("locked_until").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Enum("refund_policy").Values("timed_original_channel", "never"),
		field.String("status").MaxLen(32).Default("active"),
		field.String("source_reference").Optional().Nillable().MaxLen(191),
		field.String("notes").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Int64("created_by").Optional().Nillable(),
	}
}

func (SecurityDepositLot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("payment_order_id").Unique().Annotations(entsql.IndexWhere("payment_order_id IS NOT NULL")),
		index.Fields("user_id", "bucket_type", "created_at"),
		index.Fields("user_id", "status"),
	}
}

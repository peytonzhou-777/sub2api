package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UserLimitedCreditGrant 表示用户持有的一份限时额度。
// 每条记录独立计算额度、冻结金额和过期时间，扣费时按过期时间优先消耗。
type UserLimitedCreditGrant struct {
	ent.Schema
}

func (UserLimitedCreditGrant) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_limited_credit_grants"},
	}
}

func (UserLimitedCreditGrant) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("source_type").
			MaxLen(32).
			Default("redeem_code"),
		field.Int64("source_id").
			Optional().
			Nillable(),
		field.Float("initial_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Float("used_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Float("frozen_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0),
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("status").
			MaxLen(20).
			Default("active"),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UserLimitedCreditGrant) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("limited_credit_grants").
			Field("user_id").
			Required().
			Unique(),
		edge.To("ledger_entries", UserLimitedCreditLedger.Type),
	}
}

func (UserLimitedCreditGrant) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "status", "expires_at"),
		index.Fields("source_type", "source_id").
			StorageKey("idx_user_limited_credit_grants_source"),
		index.Fields("source_type", "source_id").
			Unique().
			StorageKey("idx_user_limited_credit_grants_recharge_bonus_order").
			Annotations(entsql.IndexWhere("source_type = 'recharge_bonus' AND source_id IS NOT NULL")),
		index.Fields("source_type", "source_id", "user_id").
			Unique().
			StorageKey("idx_user_limited_credit_grants_recurring_user").
			Annotations(entsql.IndexWhere("source_type = 'recurring_grant' AND source_id IS NOT NULL")),
	}
}

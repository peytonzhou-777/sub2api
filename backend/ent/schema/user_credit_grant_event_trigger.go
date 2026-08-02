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

// UserCreditGrantEventTrigger 记录用户成功触发事件时的实际发放结果。
type UserCreditGrantEventTrigger struct {
	ent.Schema
}

func (UserCreditGrantEventTrigger) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_credit_grant_event_triggers"},
	}
}

// Fields 保存发放快照，使事件后续修改不会改变历史事实。
func (UserCreditGrantEventTrigger) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("event_id"),
		field.Int64("user_id"),
		field.String("credit_type_snapshot").
			MaxLen(16),
		field.Float("amount_snapshot").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Int("validity_days_snapshot").
			Optional().
			Nillable(),
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int64("balance_history_id").
			Optional().
			Nillable(),
		field.Int64("limited_credit_grant_id").
			Optional().
			Nillable(),
		field.Time("triggered_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UserCreditGrantEventTrigger) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("event", CreditGrantEvent.Type).
			Unique().
			Required().
			Field("event_id").
			Annotations(entsql.OnDelete(entsql.Restrict)),
		edge.To("user", User.Type).
			Unique().
			Required().
			Field("user_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("balance_history", RedeemCode.Type).
			Unique().
			Field("balance_history_id").
			Annotations(entsql.OnDelete(entsql.SetNull)),
		edge.To("limited_credit_grant", UserLimitedCreditGrant.Type).
			Unique().
			Field("limited_credit_grant_id").
			Annotations(entsql.OnDelete(entsql.SetNull)),
	}
}

func (UserCreditGrantEventTrigger) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_id", "user_id").Unique(),
		index.Fields("user_id", "triggered_at"),
		index.Fields("event_id", "triggered_at"),
	}
}

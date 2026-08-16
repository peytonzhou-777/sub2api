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

// SecurityDepositRiskEvent 记录倍率变化历史，事件只追加不覆盖。
type SecurityDepositRiskEvent struct {
	ent.Schema
}

func (SecurityDepositRiskEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_risk_events"}}
}

func (SecurityDepositRiskEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Enum("event_type").Values("cyber_escalation", "admin_adjustment"),
		field.Int64("violation_id").Optional().Nillable(),
		field.Int64("strike_count_before").Min(0),
		field.Int64("strike_count_after").Min(0),
		field.Int64("multiplier_before").Min(1),
		field.Int64("multiplier_after").Min(1),
		field.Int64("operator_id").Optional().Nillable(),
		field.String("reason").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("idempotency_key").MaxLen(191).Unique(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SecurityDepositRiskEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("violation_id").Unique().Annotations(entsql.IndexWhere("violation_id IS NOT NULL")),
	}
}

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

// SecurityDepositViolation 保存可信官方网安事件的脱敏处罚事实。
type SecurityDepositViolation struct {
	ent.Schema
}

func (SecurityDepositViolation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_violations"}}
}

func (SecurityDepositViolation) Fields() []ent.Field {
	return []ent.Field{
		field.String("event_key").MaxLen(191).Unique(),
		field.String("request_id").MaxLen(191),
		field.String("upstream_response_id").Optional().Nillable().MaxLen(191),
		field.Int64("turn_index").Optional().Nillable().Min(0),
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("group_id"),
		field.String("policy_code").MaxLen(64),
		field.String("detector_version").MaxLen(64),
		field.Int64("base_required_snapshot_cents").Min(0),
		field.Int64("risk_multiplier_before").Min(1),
		field.Int64("required_snapshot_cents").Min(0),
		field.Int64("risk_multiplier_after").Min(1),
		field.Int64("forfeited_cents").Default(0).Min(0),
		field.Int64("shortfall_cents").Default(0).Min(0),
		field.String("state").MaxLen(32),
		field.String("error_code").Optional().Nillable().MaxLen(64),
		field.Int("retry_count").Default(0).Min(0),
		field.String("api_key_name_snapshot").MaxLen(100),
		field.String("group_name_snapshot").MaxLen(100),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("processed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SecurityDepositViolation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("api_key_id", "created_at"),
		index.Fields("state", "created_at"),
	}
}

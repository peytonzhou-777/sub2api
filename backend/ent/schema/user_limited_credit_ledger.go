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

// UserLimitedCreditLedger 记录限时额度发放、扣减、冻结、结算和释放流水。
// 流水用于审计和按 batch_id 还原批量生图冻结分配。
type UserLimitedCreditLedger struct {
	ent.Schema
}

func (UserLimitedCreditLedger) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_limited_credit_ledger"},
	}
}

func (UserLimitedCreditLedger) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("grant_id"),
		field.String("event_type").
			MaxLen(32),
		field.Float("amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.String("request_id").
			Optional().
			Nillable().
			MaxLen(128),
		field.Int64("api_key_id").
			Optional().
			Nillable(),
		field.String("batch_id").
			Optional().
			Nillable().
			MaxLen(128),
		field.Int64("usage_log_id").
			Optional().
			Nillable(),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UserLimitedCreditLedger) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("limited_credit_ledger_entries").
			Field("user_id").
			Required().
			Unique(),
		edge.From("grant", UserLimitedCreditGrant.Type).
			Ref("ledger_entries").
			Field("grant_id").
			Required().
			Unique(),
	}
}

func (UserLimitedCreditLedger) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("grant_id", "event_type"),
		index.Fields("batch_id"),
		index.Fields("request_id", "api_key_id"),
	}
}

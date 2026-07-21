package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RecurringCreditUserItem 保存循环赠额批次的逐用户资格与发放结果。
type RecurringCreditUserItem struct{ ent.Schema }

func (RecurringCreditUserItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "recurring_credit_user_items"}}
}

func (RecurringCreditUserItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"),
		field.Int64("user_id"),
		field.String("email").MaxLen(255).Default(""),
		field.String("username").MaxLen(100).Default(""),
		field.String("user_status").MaxLen(20).Default(""),
		field.Bool("user_deleted").Default(false),
		field.Float("actual_cost").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Float("net_recharge").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Time("api_last_used_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("site_last_active_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("qualification_reason").MaxLen(32).Default(""),
		field.Float("grant_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Int64("grant_id").Optional().Nillable(),
		field.String("result").MaxLen(32),
		field.String("exclusion_reason").MaxLen(100).Default(""),
	}
}

func (RecurringCreditUserItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id", "user_id").Unique(),
		index.Fields("batch_id", "result"),
		index.Fields("batch_id", "email"),
		index.Fields("batch_id", "username"),
	}
}

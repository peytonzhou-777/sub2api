package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ResetRebateUserItem 保存逐用户消费快照及最终发放结果。
type ResetRebateUserItem struct{ ent.Schema }

func (ResetRebateUserItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_user_items"}}
}

func (ResetRebateUserItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"), field.Int64("user_id"),
		field.String("email").MaxLen(255).Default(""),
		field.String("username").MaxLen(100).Default(""),
		field.String("user_status").MaxLen(20).Default(""),
		field.Bool("user_deleted").Default(false),
		field.Float("actual_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Int("rebate_ratio").Optional().Nillable(),
		field.Float("rebate_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Bool("issued").Default(false),
		field.String("exclusion_reason").MaxLen(100).Default(""),
		field.Int64("grant_id").Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateUserItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id", "user_id").Unique(),
		index.Fields("batch_id", "rebate_amount"),
		index.Fields("batch_id", "email"),
		index.Fields("batch_id", "username"),
	}
}

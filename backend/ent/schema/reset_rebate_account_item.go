package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ResetRebateAccountItem 保存统计周期内承载过消费的账号快照。
type ResetRebateAccountItem struct{ ent.Schema }

func (ResetRebateAccountItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_account_items"}}
}

func (ResetRebateAccountItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"), field.Int64("account_id"),
		field.String("account_name").MaxLen(100).Default(""),
		field.String("platform").MaxLen(50).Default(""),
		field.String("account_type").MaxLen(20).Default(""),
		field.Bool("is_shadow").Default(false),
		field.Bool("in_group").Default(false),
		field.Bool("schedulable").Default(false),
		field.Float("consumed_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default(0),
		field.Int("available_count").Optional().Nillable(),
		field.Float("weekly_used_percent").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(12,8)"}),
		field.Int64("weekly_window_seconds").Optional().Nillable(),
		field.Bool("included").Default(false),
		field.String("exclusion_reason").MaxLen(100).Default(""),
		field.String("error_code").MaxLen(64).Default(""),
		field.String("error_message").MaxLen(240).Default(""),
		field.Time("fetched_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateAccountItem) Indexes() []ent.Index {
	return []ent.Index{index.Fields("batch_id", "account_id").Unique(), index.Fields("batch_id", "included")}
}

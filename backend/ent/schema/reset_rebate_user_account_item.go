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

// ResetRebateUserAccountItem 保存逐用户逐账号的高精度消费贡献。
type ResetRebateUserAccountItem struct{ ent.Schema }

func (ResetRebateUserAccountItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_user_account_items"}}
}

// Fields 定义贡献快照字段。
func (ResetRebateUserAccountItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"),
		field.Int64("user_id"),
		field.Int64("account_id"),
		field.String("account_name").MaxLen(255),
		field.Time("period_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("period_end").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("raw_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.String("effective_stat_ratio").SchemaType(map[string]string{dialect.Postgres: "decimal(11,8)"}).Default("0"),
		field.String("weighted_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateUserAccountItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id", "user_id", "account_id").Unique(),
		index.Fields("batch_id", "user_id"),
	}
}

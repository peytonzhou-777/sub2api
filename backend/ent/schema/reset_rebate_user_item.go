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

// ResetRebateUserItem 保存逐用户结算结果和失败状态。
type ResetRebateUserItem struct{ ent.Schema }

func (ResetRebateUserItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_user_items"}}
}

// Fields 定义用户快照、金额、结果和重试审计摘要。
func (ResetRebateUserItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"),
		field.Int64("user_id"),
		field.String("email").MaxLen(255).Default(""),
		field.String("username").MaxLen(100).Default(""),
		field.String("user_status").MaxLen(20).Default(""),
		field.Bool("user_deleted").Default(false),
		field.String("raw_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.String("weighted_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.String("expected_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.String("actual_issued_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).Default("0"),
		field.String("result").MaxLen(20).Default("pending"),
		field.String("exclusion_reason").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Bool("skip_count_consumed").Default(false),
		field.String("error_code").MaxLen(64).Default(""),
		field.String("error_message").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Int("attempt_count").Default(0),
		field.Time("first_failed_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_attempt_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Int64("grant_id").Optional().Nillable(),
		field.Time("issued_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateUserItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id", "user_id").Unique(),
		index.Fields("batch_id", "result", "user_id"),
	}
}

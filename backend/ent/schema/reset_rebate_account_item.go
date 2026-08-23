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

// ResetRebateAccountItem 保存批次内不可变的账号窗口和统计比例快照。
type ResetRebateAccountItem struct{ ent.Schema }

func (ResetRebateAccountItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "reset_rebate_account_items"}}
}

// Fields 定义账号快照、窗口、比例和金额字段。
func (ResetRebateAccountItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("batch_id"),
		field.Int64("account_id"),
		field.String("account_name").MaxLen(255),
		field.String("platform").MaxLen(32),
		field.String("account_type").MaxLen(32),
		field.Bool("is_shadow").Default(false),
		field.String("account_status").MaxLen(20).Default(""),
		field.String("account_error_message").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.Bool("schedulable").Default(true),
		field.Time("period_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("period_end").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("default_window_source").MaxLen(32).Default("history"),
		field.String("window_risk").MaxLen(32).Default(""),
		field.String("ratio_mode").MaxLen(16).Default("auto"),
		field.String("auto_stat_ratio").SchemaType(map[string]string{dialect.Postgres: "decimal(11,8)"}).Default("0"),
		field.String("manual_stat_ratio").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "decimal(11,8)"}),
		field.String("effective_stat_ratio").SchemaType(map[string]string{dialect.Postgres: "decimal(11,8)"}).Default("0"),
		field.Bool("included_in_statistics").Default(true),
		field.String("statistics_exclusion_reason").SchemaType(map[string]string{dialect.Postgres: "text"}).Default(""),
		field.String("raw_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.String("weighted_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(30,16)"}).Default("0"),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ResetRebateAccountItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_id", "account_id").Unique(),
		index.Fields("account_id", "period_start", "period_end"),
	}
}

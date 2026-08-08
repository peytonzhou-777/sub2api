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

// AccountUsageWindowHistory 保存账号实际观察到的用量窗口开始时间。
type AccountUsageWindowHistory struct{ ent.Schema }

func (AccountUsageWindowHistory) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "account_usage_window_histories"}}
}

// Fields 定义追加式账号窗口历史字段。
func (AccountUsageWindowHistory) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("account_id"),
		field.String("window_kind").MaxLen(32).Default("codex_7d"),
		field.Time("window_started_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("first_observed_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_observed_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("source_type").MaxLen(32).Default("usage_refresh"),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (AccountUsageWindowHistory) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "window_kind", "window_started_at").Unique(),
	}
}

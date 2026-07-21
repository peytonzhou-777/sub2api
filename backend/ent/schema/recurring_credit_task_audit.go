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

// RecurringCreditTaskAudit 记录管理员对循环任务的不可变操作审计。
type RecurringCreditTaskAudit struct{ ent.Schema }

func (RecurringCreditTaskAudit) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "recurring_credit_task_audits"}}
}

func (RecurringCreditTaskAudit) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("task_id"),
		field.Int64("admin_id"),
		field.String("admin_email").MaxLen(255).Default(""),
		field.String("client_ip").MaxLen(64).Default(""),
		field.String("action").MaxLen(32),
		field.JSON("before_snapshot", map[string]any{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("after_snapshot", map[string]any{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (RecurringCreditTaskAudit) Indexes() []ent.Index {
	return []ent.Index{index.Fields("task_id", "created_at"), index.Fields("admin_id", "created_at")}
}

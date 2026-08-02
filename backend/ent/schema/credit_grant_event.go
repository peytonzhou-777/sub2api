package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CreditGrantEvent 定义管理员可向单个用户手工触发的赠额事件。
type CreditGrantEvent struct {
	ent.Schema
}

func (CreditGrantEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "credit_grant_events"},
	}
}

func (CreditGrantEvent) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields 定义事件当前配置；历史实际发放配置由触发记录保存。
func (CreditGrantEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("credit_type").
			MaxLen(16),
		field.Float("amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}),
		field.Int("validity_days").
			Optional().
			Nillable(),
	}
}

func (CreditGrantEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_at"),
		index.Fields("deleted_at"),
	}
}

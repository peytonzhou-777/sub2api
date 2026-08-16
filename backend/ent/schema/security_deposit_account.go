package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SecurityDepositAccount 保存用户单个保证金资金桶的权威汇总。
type SecurityDepositAccount struct {
	ent.Schema
}

func (SecurityDepositAccount) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_accounts"}}
}

func (SecurityDepositAccount) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (SecurityDepositAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Enum("bucket_type").Values("paid", "admin_grant"),
		field.String("currency").MaxLen(3).Default("CNY"),
		field.Int64("balance_cents").Default(0).Min(0),
		field.Int64("refund_reserved_cents").Default(0).Min(0),
		field.Int64("version").Default(1).Min(1),
	}
}

func (SecurityDepositAccount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "bucket_type").Unique(),
		index.Fields("user_id"),
	}
}

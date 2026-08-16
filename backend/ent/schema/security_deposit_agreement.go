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

// SecurityDepositAgreement 保存用户接受保证金规则的服务端证据。
type SecurityDepositAgreement struct {
	ent.Schema
}

func (SecurityDepositAgreement) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "security_deposit_agreements"}}
}

func (SecurityDepositAgreement) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("policy_version").MaxLen(64),
		field.String("content_hash").MaxLen(128),
		field.Int64("group_id"),
		field.Int64("base_required_snapshot_cents").Min(0),
		field.Int64("risk_multiplier_snapshot").Min(1),
		field.Int64("required_snapshot_cents").Min(0),
		field.Time("accepted_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("client_ip").MaxLen(64),
		field.String("user_agent").SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (SecurityDepositAgreement) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "group_id", "policy_version", "content_hash").Unique(),
		index.Fields("user_id", "accepted_at"),
	}
}

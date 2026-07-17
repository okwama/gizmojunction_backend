// Package audit exposes the admin audit-log read endpoint. Writes happen
// via database triggers (see the audit_triggers migration), not through
// this API — the frontend's logAudit helper was dead code and was removed
// during the Phase 5 migration.
package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/auth"
)

type User struct {
	ID       string  `json:"id"`
	FullName *string `json:"full_name,omitempty"`
	Email    *string `json:"email,omitempty"`
}

type Log struct {
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Resource  string          `json:"resource"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	IPAddress *string         `json:"ip_address,omitempty"`
	UserAgent *string         `json:"user_agent,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	User      *User           `json:"user,omitempty"`
}

type ListInput struct {
	Authorization string `header:"Authorization"`
	Limit         int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
}

type ListOutput struct {
	Body struct {
		Logs []Log `json:"logs"`
	}
}

func Register(api huma.API, pool *pgxpool.Pool, authSvc *auth.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-list-audit-logs",
		Method:      http.MethodGet,
		Path:        "/v1/admin/audit-logs",
		Summary:     "Recent audit log entries with actor details (admin only)",
	}, func(ctx context.Context, input *ListInput) (*ListOutput, error) {
		if _, err := authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
			return nil, err
		}

		rows, err := pool.Query(ctx, `
			SELECT a.id::text, a.action, a.resource, a.metadata, a.ip_address, a.user_agent, a.created_at,
				p.id::text, p.full_name, p.email
			FROM audit_logs a
			LEFT JOIN profiles p ON p.id = a.user_id
			ORDER BY a.created_at DESC
			LIMIT $1`, input.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load audit logs", err)
		}
		defer rows.Close()

		out := &ListOutput{}
		out.Body.Logs = []Log{}
		for rows.Next() {
			var l Log
			var userID, fullName, email *string
			if err := rows.Scan(&l.ID, &l.Action, &l.Resource, &l.Metadata, &l.IPAddress, &l.UserAgent, &l.CreatedAt, &userID, &fullName, &email); err != nil {
				return nil, huma.Error500InternalServerError("failed to read audit logs", err)
			}
			if userID != nil {
				l.User = &User{ID: *userID, FullName: fullName, Email: email}
			}
			out.Body.Logs = append(out.Body.Logs, l)
		}
		if err := rows.Err(); err != nil {
			return nil, huma.Error500InternalServerError("failed to read audit logs", err)
		}
		return out, nil
	})
}

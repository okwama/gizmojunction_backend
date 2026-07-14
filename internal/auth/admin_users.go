package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// This file ports the admin "create a user" and "change a user's role"
// flows — supabase.auth.admin.createUser() and a direct profiles.role
// update can't work anymore once this backend, not Supabase, verifies
// logins. Both are ADMIN-only.

type CreateUserInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Email    string `json:"email" format:"email"`
		Password string `json:"password" minLength:"8"`
		FullName string `json:"full_name"`
		Phone    string `json:"phone,omitempty"`
		Role     string `json:"role,omitempty" enum:"ADMIN,CUSTOMER"`
	}
}

type CreateUserOutput struct {
	Body UserOut
}

func (s *Service) CreateUser(ctx context.Context, input *CreateUserInput) (*CreateUserOutput, error) {
	if _, err := s.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}

	email := strings.ToLower(strings.TrimSpace(input.Body.Email))
	if email == "" || input.Body.Password == "" || input.Body.FullName == "" {
		return nil, huma.Error400BadRequest("email, password, and full_name are required")
	}
	role := input.Body.Role
	if role == "" {
		role = "CUSTOMER"
	}

	if _, err := s.repo.GetProfileByEmail(ctx, email); err == nil {
		return nil, huma.Error409Conflict("an account with this email already exists")
	} else if !errors.Is(err, ErrNotFound) {
		return nil, huma.Error500InternalServerError("lookup failed", err)
	}

	hash, err := HashPassword(input.Body.Password)
	if err != nil {
		return nil, huma.Error500InternalServerError("hash failed", err)
	}

	profile, err := s.repo.CreateProfile(ctx, email, input.Body.FullName, input.Body.Phone, role, hash)
	if err != nil {
		return nil, huma.Error500InternalServerError("create profile failed", err)
	}

	out := &CreateUserOutput{}
	out.Body = toUserOut(profile)
	return out, nil
}

type UpdateRoleInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Role string `json:"role" enum:"ADMIN,CUSTOMER"`
	}
}

type UpdateRoleOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (s *Service) UpdateRole(ctx context.Context, input *UpdateRoleInput) (*UpdateRoleOutput, error) {
	if _, err := s.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}

	if err := s.repo.UpdateRole(ctx, input.ID, input.Body.Role); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, huma.Error404NotFound("user not found")
		}
		return nil, huma.Error500InternalServerError("update role failed", err)
	}

	out := &UpdateRoleOutput{}
	out.Body.Success = true
	return out, nil
}

type ListUsersInput struct {
	Authorization string `header:"Authorization"`
}

type ListUsersOutput struct {
	Body struct {
		Users []ProfileSummary `json:"users"`
	}
}

func (s *Service) ListUsers(ctx context.Context, input *ListUsersInput) (*ListUsersOutput, error) {
	if _, err := s.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}

	profiles, err := s.repo.ListProfiles(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("list users failed", err)
	}

	out := &ListUsersOutput{}
	out.Body.Users = profiles
	return out, nil
}

func RegisterAdminUsers(api huma.API, s *Service) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-list-users",
		Method:      "GET",
		Path:        "/v1/admin/users",
		Summary:     "List all user profiles (admin only)",
	}, s.ListUsers)

	huma.Register(api, huma.Operation{
		OperationID: "admin-create-user",
		Method:      "POST",
		Path:        "/v1/admin/users",
		Summary:     "Create a user account with a specific role (admin only)",
	}, s.CreateUser)

	huma.Register(api, huma.Operation{
		OperationID: "admin-update-user-role",
		Method:      "PATCH",
		Path:        "/v1/admin/users/{id}/role",
		Summary:     "Change a user's role (admin only)",
	}, s.UpdateRole)
}

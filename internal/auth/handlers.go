package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

const RefreshTokenTTL = 30 * 24 * time.Hour

type Service struct {
	repo      *Repo
	jwtSecret []byte
}

func NewService(repo *Repo, jwtSecret string) *Service {
	return &Service{repo: repo, jwtSecret: []byte(jwtSecret)}
}

type UserOut struct {
	ID       string `json:"id"`
	Email    string `json:"email,omitempty"`
	FullName string `json:"full_name,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Role     string `json:"role"`
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type AuthResponse struct {
	Body struct {
		TokenPair
		User UserOut `json:"user"`
	}
}

func toUserOut(p Profile) UserOut {
	out := UserOut{ID: p.ID, Role: p.Role}
	if p.Email != nil {
		out.Email = *p.Email
	}
	if p.FullName != nil {
		out.FullName = *p.FullName
	}
	if p.Phone != nil {
		out.Phone = *p.Phone
	}
	return out
}

func (s *Service) issueTokens(ctx context.Context, p Profile) (TokenPair, error) {
	access, err := GenerateAccessToken(s.jwtSecret, p.ID, out(p.Email), p.Role, out(p.FullName), out(p.Phone))
	if err != nil {
		return TokenPair{}, err
	}
	rawRefresh, hash, err := NewRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	if err := s.repo.CreateRefreshToken(ctx, p.ID, hash, time.Now().Add(RefreshTokenTTL)); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:  access,
		RefreshToken: rawRefresh,
		ExpiresIn:    int(AccessTokenTTL.Seconds()),
	}, nil
}

func out(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- signup ---

type SignupInput struct {
	Body struct {
		Email    string `json:"email" format:"email"`
		Password string `json:"password" minLength:"8"`
		FullName string `json:"full_name,omitempty"`
		Phone    string `json:"phone,omitempty"`
	}
}

func (s *Service) Signup(ctx context.Context, input *SignupInput) (*AuthResponse, error) {
	email := strings.ToLower(strings.TrimSpace(input.Body.Email))
	if email == "" || input.Body.Password == "" {
		return nil, huma.Error400BadRequest("email and password are required")
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

	profile, err := s.repo.CreateProfile(ctx, email, input.Body.FullName, input.Body.Phone, "CUSTOMER", hash)
	if err != nil {
		return nil, huma.Error500InternalServerError("create profile failed", err)
	}

	tokens, err := s.issueTokens(ctx, profile)
	if err != nil {
		return nil, huma.Error500InternalServerError("issue tokens failed", err)
	}

	resp := &AuthResponse{}
	resp.Body.TokenPair = tokens
	resp.Body.User = toUserOut(profile)
	return resp, nil
}

// --- login ---

type LoginInput struct {
	Body struct {
		Email    string `json:"email" format:"email"`
		Password string `json:"password"`
	}
}

func (s *Service) Login(ctx context.Context, input *LoginInput) (*AuthResponse, error) {
	email := strings.ToLower(strings.TrimSpace(input.Body.Email))

	profile, err := s.repo.GetProfileByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, huma.Error401Unauthorized("invalid email or password")
		}
		return nil, huma.Error500InternalServerError("lookup failed", err)
	}

	if profile.PasswordHash == nil {
		return nil, huma.Error401Unauthorized("invalid email or password")
	}

	var valid bool
	switch profile.PasswordAlgo {
	case "bcrypt":
		valid = VerifyBcrypt(input.Body.Password, *profile.PasswordHash)
		if valid {
			// Transparent upgrade — matches the plan's "verify existing
			// GoTrue bcrypt hash and upgrade to Argon2id on first login"
			// approach, avoiding a forced mass password reset.
			if newHash, hashErr := HashPassword(input.Body.Password); hashErr == nil {
				_ = s.repo.UpgradePasswordHash(ctx, profile.ID, newHash)
			}
		}
	default:
		valid, err = VerifyArgon2id(input.Body.Password, *profile.PasswordHash)
		if err != nil {
			valid = false
		}
	}

	if !valid {
		return nil, huma.Error401Unauthorized("invalid email or password")
	}
	if !profile.IsActive {
		return nil, huma.Error403Forbidden("account is deactivated")
	}

	tokens, err := s.issueTokens(ctx, profile)
	if err != nil {
		return nil, huma.Error500InternalServerError("issue tokens failed", err)
	}

	resp := &AuthResponse{}
	resp.Body.TokenPair = tokens
	resp.Body.User = toUserOut(profile)
	return resp, nil
}

// --- refresh ---

type RefreshInput struct {
	Body struct {
		RefreshToken string `json:"refresh_token"`
	}
}

func (s *Service) Refresh(ctx context.Context, input *RefreshInput) (*AuthResponse, error) {
	hash := HashRefreshToken(input.Body.RefreshToken)

	rt, err := s.repo.GetRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, huma.Error401Unauthorized("invalid refresh token")
		}
		return nil, huma.Error500InternalServerError("lookup failed", err)
	}
	if rt.RevokedAt != nil || time.Now().After(rt.ExpiresAt) {
		return nil, huma.Error401Unauthorized("refresh token expired or revoked")
	}

	profile, err := s.repo.GetProfileByID(ctx, rt.ProfileID)
	if err != nil {
		return nil, huma.Error401Unauthorized("account not found")
	}
	if !profile.IsActive {
		return nil, huma.Error403Forbidden("account is deactivated")
	}

	// Rotate: revoke the used token, issue a fresh pair.
	_ = s.repo.RevokeRefreshToken(ctx, hash)

	tokens, err := s.issueTokens(ctx, profile)
	if err != nil {
		return nil, huma.Error500InternalServerError("issue tokens failed", err)
	}

	resp := &AuthResponse{}
	resp.Body.TokenPair = tokens
	resp.Body.User = toUserOut(profile)
	return resp, nil
}

// --- logout ---

type LogoutInput struct {
	Body struct {
		RefreshToken string `json:"refresh_token"`
	}
}

type LogoutOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (s *Service) Logout(ctx context.Context, input *LogoutInput) (*LogoutOutput, error) {
	if input.Body.RefreshToken != "" {
		_ = s.repo.RevokeRefreshToken(ctx, HashRefreshToken(input.Body.RefreshToken))
	}
	out := &LogoutOutput{}
	out.Body.Success = true
	return out, nil
}

// --- me ---

type MeInput struct {
	Authorization string `header:"Authorization"`
}

type MeOutput struct {
	Body UserOut
}

func (s *Service) Me(ctx context.Context, input *MeInput) (*MeOutput, error) {
	claims, err := s.requireBearer(input.Authorization)
	if err != nil {
		return nil, err
	}
	profile, err := s.repo.GetProfileByID(ctx, claims.ProfileID)
	if err != nil {
		return nil, huma.Error401Unauthorized("account not found")
	}
	out := &MeOutput{}
	out.Body = toUserOut(profile)
	return out, nil
}

// requireBearer is the shared entry point other packages (e.g. internal/ai)
// use to protect an endpoint: extract+verify the JWT locally, no DB or
// network round trip needed for the common case.
func (s *Service) requireBearer(authHeader string) (*Claims, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return nil, huma.Error401Unauthorized("missing bearer token")
	}
	claims, err := VerifyAccessToken(s.jwtSecret, strings.TrimPrefix(authHeader, prefix))
	if err != nil {
		return nil, huma.Error401Unauthorized(fmt.Sprintf("invalid token: %v", err))
	}
	return claims, nil
}

// Authenticate verifies the bearer token for any role — used by endpoints
// that only need to know who the caller is (own-account operations).
func (s *Service) Authenticate(authHeader string) (*Claims, error) {
	return s.requireBearer(authHeader)
}

// RequireRole is called by handlers in other packages that need to gate an
// endpoint to a specific role (e.g. the AI admin endpoints).
func (s *Service) RequireRole(authHeader, role string) (*Claims, error) {
	claims, err := s.requireBearer(authHeader)
	if err != nil {
		return nil, err
	}
	if claims.Role != role {
		return nil, huma.Error403Forbidden("insufficient permissions")
	}
	return claims, nil
}

func Register(api huma.API, s *Service) {
	huma.Register(api, huma.Operation{
		OperationID: "auth-signup",
		Method:      "POST",
		Path:        "/v1/auth/signup",
		Summary:     "Create a customer account",
	}, s.Signup)

	huma.Register(api, huma.Operation{
		OperationID: "auth-login",
		Method:      "POST",
		Path:        "/v1/auth/login",
		Summary:     "Log in with email and password",
	}, s.Login)

	huma.Register(api, huma.Operation{
		OperationID: "auth-refresh",
		Method:      "POST",
		Path:        "/v1/auth/refresh",
		Summary:     "Exchange a refresh token for a new token pair",
	}, s.Refresh)

	huma.Register(api, huma.Operation{
		OperationID: "auth-logout",
		Method:      "POST",
		Path:        "/v1/auth/logout",
		Summary:     "Revoke a refresh token",
	}, s.Logout)

	huma.Register(api, huma.Operation{
		OperationID: "auth-me",
		Method:      "GET",
		Path:        "/v1/auth/me",
		Summary:     "Current authenticated user",
	}, s.Me)
}

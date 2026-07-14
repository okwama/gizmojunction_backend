package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Profile struct {
	ID           string  `db:"id"`
	Email        *string `db:"email"`
	FullName     *string `db:"full_name"`
	Phone        *string `db:"phone"`
	Role         string  `db:"role"`
	PasswordHash *string `db:"password_hash"`
	PasswordAlgo string  `db:"password_algo"`
	IsActive     bool    `db:"is_active"`
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var ErrNotFound = errors.New("not found")

const profileColumns = `id::text, email, full_name, phone, role::text, password_hash, password_algo, is_active`

func (r *Repo) GetProfileByEmail(ctx context.Context, email string) (Profile, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+profileColumns+` FROM profiles WHERE lower(email) = lower($1)`, email)
	if err != nil {
		return Profile{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Profile])
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return p, err
}

func (r *Repo) GetProfileByID(ctx context.Context, id string) (Profile, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+profileColumns+` FROM profiles WHERE id = $1`, id)
	if err != nil {
		return Profile{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Profile])
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return p, err
}

func (r *Repo) CreateProfile(ctx context.Context, email, fullName, phone, role, passwordHash string) (Profile, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO profiles (id, email, full_name, phone, role, password_hash, password_algo)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, 'argon2id')
		RETURNING `+profileColumns,
		email, fullName, phone, role, passwordHash,
	)
	if err != nil {
		return Profile{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Profile])
}

func (r *Repo) UpgradePasswordHash(ctx context.Context, profileID, newHash string) error {
	_, err := r.pool.Exec(ctx, `UPDATE profiles SET password_hash = $1, password_algo = 'argon2id' WHERE id = $2`, newHash, profileID)
	return err
}

type ProfileSummary struct {
	ID            string     `db:"id" json:"id"`
	Email         *string    `db:"email" json:"email"`
	FullName      *string    `db:"full_name" json:"full_name"`
	Phone         *string    `db:"phone" json:"phone"`
	Role          string     `db:"role" json:"role"`
	LoyaltyPoints int32      `db:"loyalty_points" json:"loyalty_points"`
	IsActive      bool       `db:"is_active" json:"is_active"`
	CreatedAt     *time.Time `db:"created_at" json:"created_at"`
}

func (r *Repo) ListProfiles(ctx context.Context) ([]ProfileSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, email, full_name, phone, role::text, loyalty_points, is_active, created_at
		FROM profiles ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProfileSummary])
}

func (r *Repo) UpdateRole(ctx context.Context, profileID, role string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE profiles SET role = $1 WHERE id = $2`, role, profileID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type RefreshToken struct {
	ID        string     `db:"id"`
	ProfileID string     `db:"profile_id"`
	ExpiresAt time.Time  `db:"expires_at"`
	RevokedAt *time.Time `db:"revoked_at"`
}

func (r *Repo) CreateRefreshToken(ctx context.Context, profileID, tokenHash string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (profile_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, profileID, tokenHash, expiresAt)
	return err
}

func (r *Repo) GetRefreshToken(ctx context.Context, tokenHash string) (RefreshToken, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, profile_id::text, expires_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1
	`, tokenHash)
	if err != nil {
		return RefreshToken{}, err
	}
	rt, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[RefreshToken])
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshToken{}, ErrNotFound
	}
	return rt, err
}

func (r *Repo) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := r.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return err
}

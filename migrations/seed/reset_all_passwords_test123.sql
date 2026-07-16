-- Resets every profile's password to "Test123" (Argon2id, matching
-- backend/internal/auth's hashing scheme). Generated via:
--   go run ./cmd/hashpw Test123
-- Dev/test use only — every account gets the SAME hash (same salt too),
-- which is fine for a throwaway reset but should never be run in
-- production.

UPDATE profiles
SET password_hash = 'argon2id$1$65536$4$dWO6N6v3MvREYAgGEEAD0w$EwSpDAcQEK4srfIFsp+vBaf3rnlg73W+cNwge3+9v0g',
    password_algo = 'argon2id';

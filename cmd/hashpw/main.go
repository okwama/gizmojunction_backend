// hashpw prints an Argon2id hash for a given plaintext password, in the
// exact format backend/internal/auth expects in profiles.password_hash.
// Useful for manual password resets via SQL when the admin UI itself is
// unavailable (e.g. bootstrapping the first admin, or a bulk reset).
package main

import (
	"fmt"
	"os"

	"gizmojunction/backend/internal/auth"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hashpw <password>")
		os.Exit(1)
	}
	hash, err := auth.HashPassword(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash failed:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}

// One-time backfill: re-host Supabase-Storage-hosted images on Cloudinary
// and update the Neon rows, so the storefront's last runtime dependency on
// Supabase (slow cold-start image loads) goes away. Signed upload via the
// Cloudinary API with file=<remote url> — Cloudinary fetches the source
// itself, nothing is downloaded locally. Deleted after the cutover.
//
// Usage:
//	go run ./cmd/imgbackfill          # dry run: counts + samples only
//	go run ./cmd/imgbackfill -apply   # actually upload and update rows
package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/config"
)

const supabaseMarker = "supabase.co/storage"

type cloudinary struct {
	cloud, key, secret string
	clockOffset        time.Duration // Cloudinary server time minus local time
}

// syncClock measures the skew between this machine and Cloudinary's servers
// via the Date response header — the local clock here is over an hour off,
// and Cloudinary rejects signature timestamps more than 1h stale.
func (c *cloudinary) syncClock(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://api.cloudinary.com/v1_1/"+c.cloud+"/image/upload", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	serverTime, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return fmt.Errorf("no parseable Date header: %w", err)
	}
	c.clockOffset = time.Until(serverTime)
	fmt.Printf("Clock skew vs Cloudinary: %s\n", c.clockOffset.Round(time.Second))
	return nil
}

func (c *cloudinary) uploadFromURL(ctx context.Context, srcURL string) (string, error) {
	ts := fmt.Sprintf("%d", time.Now().Add(c.clockOffset).Unix())
	sig := sha1.Sum([]byte("timestamp=" + ts + c.secret))

	form := url.Values{}
	form.Set("file", srcURL)
	form.Set("api_key", c.key)
	form.Set("timestamp", ts)
	form.Set("signature", hex.EncodeToString(sig[:]))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.cloudinary.com/v1_1/"+c.cloud+"/image/upload",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var parsed struct {
		SecureURL string `json:"secure_url"`
		Error     struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.SecureURL == "" {
		return "", fmt.Errorf("cloudinary: %s (http %d)", parsed.Error.Message, resp.StatusCode)
	}
	return parsed.SecureURL, nil
}

func main() {
	apply := flag.Bool("apply", false, "actually upload and update rows (default is dry run)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	cld := &cloudinary{
		cloud:  os.Getenv("CLOUDINARY_CLOUD_NAME"),
		key:    os.Getenv("CLOUDINARY_API_KEY"),
		secret: os.Getenv("CLOUDINARY_API_SECRET"),
	}
	if *apply && (cld.cloud == "" || cld.key == "" || cld.secret == "") {
		log.Fatal("CLOUDINARY_CLOUD_NAME/API_KEY/API_SECRET must be set for -apply")
	}

	ctx := context.Background()
	if *apply {
		if err := cld.syncClock(ctx); err != nil {
			log.Fatalf("clock sync: %v", err)
		}
	}
	neon, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("neon connect: %v", err)
	}
	defer neon.Close()

	// --- Scope report ---
	var imgCount, galleryCount, catCount, brandCount int
	neon.QueryRow(ctx, `SELECT count(*) FROM products WHERE image_url LIKE '%'||$1||'%'`, supabaseMarker).Scan(&imgCount)
	neon.QueryRow(ctx, `SELECT count(*) FROM products WHERE EXISTS (SELECT 1 FROM unnest(gallery) g WHERE g LIKE '%'||$1||'%')`, supabaseMarker).Scan(&galleryCount)
	neon.QueryRow(ctx, `SELECT count(*) FROM categories WHERE image_url LIKE '%'||$1||'%'`, supabaseMarker).Scan(&catCount)
	neon.QueryRow(ctx, `SELECT count(*) FROM brands WHERE logo_url LIKE '%'||$1||'%'`, supabaseMarker).Scan(&brandCount)
	fmt.Printf("Supabase-hosted images — products.image_url: %d, products.gallery: %d, categories: %d, brands: %d\n",
		imgCount, galleryCount, catCount, brandCount)

	if !*apply {
		fmt.Println("Dry run only. Re-run with -apply to migrate.")
		return
	}

	// --- products.image_url ---
	rows, err := neon.Query(ctx, `SELECT id::text, image_url FROM products WHERE image_url LIKE '%'||$1||'%'`, supabaseMarker)
	if err != nil {
		log.Fatal(err)
	}
	type rec struct{ id, u string }
	var main []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.u); err != nil {
			log.Fatal(err)
		}
		main = append(main, r)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iteration: %v", err)
	}
	rows.Close()

	ok, failed := 0, 0
	for i, r := range main {
		newURL, err := cld.uploadFromURL(ctx, r.u)
		if err != nil {
			log.Printf("product %s: %v", r.id, err)
			failed++
			continue
		}
		if _, err := neon.Exec(ctx, `UPDATE products SET image_url = $2 WHERE id = $1`, r.id, newURL); err != nil {
			log.Printf("product %s update: %v", r.id, err)
			failed++
			continue
		}
		ok++
		if (i+1)%50 == 0 {
			fmt.Printf("  ...%d/%d\n", i+1, len(main))
		}
	}
	fmt.Printf("products.image_url migrated: %d ok, %d failed\n", ok, failed)

	// --- products.gallery ---
	grows, err := neon.Query(ctx, `SELECT id::text, gallery FROM products WHERE EXISTS (SELECT 1 FROM unnest(gallery) g WHERE g LIKE '%'||$1||'%')`, supabaseMarker)
	if err != nil {
		log.Fatal(err)
	}
	type grec struct {
		id      string
		gallery []string
	}
	var gals []grec
	for grows.Next() {
		var g grec
		if err := grows.Scan(&g.id, &g.gallery); err != nil {
			log.Fatal(err)
		}
		gals = append(gals, g)
	}
	if err := grows.Err(); err != nil {
		log.Fatalf("gallery iteration: %v", err)
	}
	grows.Close()

	gok, gfailed := 0, 0
	for _, g := range gals {
		changed := false
		for i, u := range g.gallery {
			if !strings.Contains(u, supabaseMarker) {
				continue
			}
			newURL, err := cld.uploadFromURL(ctx, u)
			if err != nil {
				log.Printf("product %s gallery[%d]: %v", g.id, i, err)
				gfailed++
				continue
			}
			g.gallery[i] = newURL
			changed = true
		}
		if changed {
			if _, err := neon.Exec(ctx, `UPDATE products SET gallery = $2 WHERE id = $1`, g.id, g.gallery); err != nil {
				log.Printf("product %s gallery update: %v", g.id, err)
				gfailed++
				continue
			}
			gok++
		}
	}
	fmt.Printf("products.gallery migrated: %d products ok, %d image failures\n", gok, gfailed)

	// --- categories.image_url / brands.logo_url ---
	for _, t := range []struct{ table, col string }{{"categories", "image_url"}, {"brands", "logo_url"}, {"promotions", "banner_url"}, {"blog_posts", "cover_image"}} {
		trows, err := neon.Query(ctx, `SELECT id::text, `+t.col+` FROM `+t.table+` WHERE `+t.col+` LIKE '%'||$1||'%'`, supabaseMarker)
		if err != nil {
			log.Fatal(err)
		}
		var recs []rec
		for trows.Next() {
			var r rec
			if err := trows.Scan(&r.id, &r.u); err != nil {
				log.Fatal(err)
			}
			recs = append(recs, r)
		}
		if err := trows.Err(); err != nil {
			log.Fatalf("%s iteration: %v", t.table, err)
		}
		trows.Close()
		tok := 0
		for _, r := range recs {
			newURL, err := cld.uploadFromURL(ctx, r.u)
			if err != nil {
				log.Printf("%s %s: %v", t.table, r.id, err)
				continue
			}
			if _, err := neon.Exec(ctx, `UPDATE `+t.table+` SET `+t.col+` = $2 WHERE id = $1`, r.id, newURL); err != nil {
				log.Printf("%s %s update: %v", t.table, r.id, err)
				continue
			}
			tok++
		}
		fmt.Printf("%s.%s migrated: %d/%d\n", t.table, t.col, tok, len(recs))
	}

	// --- Final verification ---
	neon.QueryRow(ctx, `SELECT count(*) FROM products WHERE image_url LIKE '%'||$1||'%'`, supabaseMarker).Scan(&imgCount)
	neon.QueryRow(ctx, `SELECT count(*) FROM products WHERE EXISTS (SELECT 1 FROM unnest(gallery) g WHERE g LIKE '%'||$1||'%')`, supabaseMarker).Scan(&galleryCount)
	fmt.Printf("Remaining Supabase URLs — image_url: %d, gallery: %d\n", imgCount, galleryCount)
}

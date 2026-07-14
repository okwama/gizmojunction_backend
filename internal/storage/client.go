// Package storage wraps Cloudflare R2 (S3-compatible) access for the
// three writers this phase covers: generate-tax-receipt, generate-lpo, and
// import-job. Those functions themselves stay on Deno for now — only the
// storage call inside them changes, from Supabase Storage to a POST against
// this package's upload endpoint.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Client struct {
	mc     *minio.Client
	bucket string
}

func NewClient(accountID, accessKeyID, secretKey, bucket string) (*Client, error) {
	endpoint := fmt.Sprintf("%s.r2.cloudflarestorage.com", accountID)
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretKey, ""),
		Secure: true,
		Region: "auto",
	})
	if err != nil {
		return nil, err
	}
	return &Client{mc: mc, bucket: bucket}, nil
}

func (c *Client) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

func (c *Client) PresignGET(ctx context.Context, key string, expiry time.Duration) (string, error) {
	u, err := c.mc.PresignedGetObject(ctx, c.bucket, key, expiry, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client wraps the MinIO SDK client with the configured bucket and endpoint.
type Client struct {
	Client   *minio.Client
	Bucket   string
	Endpoint string
}

// New creates a MinIO Client. Panics if the connection cannot be established,
// since this is a hard dependency required at startup.
func New(endpoint, accessKey, secretKey, bucket string, secure bool) *Client {
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		panic(err)
	}
	return &Client{Client: mc, Bucket: bucket, Endpoint: endpoint}
}

// Upload streams a file to the configured bucket.
func (c *Client) Upload(ctx context.Context, objectName string, reader io.Reader, size int64, contentType string) error {
	_, err := c.Client.PutObject(ctx, c.Bucket, objectName, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", objectName, err)
	}
	return nil
}

// Delete removes an object from the configured bucket.
func (c *Client) Delete(ctx context.Context, objectName string) error {
	return c.Client.RemoveObject(ctx, c.Bucket, objectName, minio.RemoveObjectOptions{})
}

// BucketExists reports whether the configured bucket is accessible.
func (c *Client) BucketExists(ctx context.Context) (bool, error) {
	exists, err := c.Client.BucketExists(ctx, c.Bucket)
	if err != nil {
		return false, fmt.Errorf("check bucket %s: %w", c.Bucket, err)
	}
	return exists, nil
}

// PresignDownload returns a time-limited URL that triggers a file-download prompt.
func (c *Client) PresignDownload(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	u, err := c.Client.PresignedGetObject(ctx, c.Bucket, objectName, expiry, url.Values{
		"response-content-disposition": {"attachment"},
	})
	if err != nil {
		return "", fmt.Errorf("presign download %s: %w", objectName, err)
	}
	return u.String(), nil
}

// PresignGetObject returns a time-limited URL suitable for streaming with HTTP range requests.
func (c *Client) PresignGetObject(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	u, err := c.Client.PresignedGetObject(ctx, c.Bucket, objectName, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("presign get %s: %w", objectName, err)
	}
	return u.String(), nil
}

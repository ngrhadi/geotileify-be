package storage

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Client struct {
    Client   *minio.Client
    Bucket   string
    Endpoint string
}

func New(endpoint, accessKey, secretKey, bucket string, secure bool) *Client {
    minioClient, err := minio.New(endpoint, &minio.Options{
        Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
        Secure: secure,
    })
    if err != nil {
        panic(err)
    }

    return &Client{
        Client:   minioClient,
        Bucket:   bucket,
        Endpoint: endpoint,
    }
}

func (c *Client) PresignGetObject(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	u, err := c.Client.PresignedGetObject(
		ctx,
		c.Bucket,
		objectName,
		expiry,
		url.Values{}, // Empty values = streaming mode, support range requests
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned GET URL for %s: %w", objectName, err)
	}
	return u.String(), nil
}

// Upload file ke MinIO
func (c *Client) Upload(ctx context.Context, objectName string, reader io.Reader, size int64, contentType string) error {
    _, err := c.Client.PutObject(ctx, c.Bucket, objectName, reader, size, minio.PutObjectOptions{
        ContentType: contentType,
    })
    if err != nil {
        return fmt.Errorf("failed to upload: %w", err)
    }
    return nil
}

func (c *Client) PresignDownload(ctx context.Context, objectName string, expiry time.Duration) (string, error) {
	u, err := c.Client.PresignedGetObject(
		ctx,
		c.Bucket,
		objectName,
		expiry,
		url.Values{
			"response-content-disposition": {"attachment"},
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL for %s: %w", objectName, err)
	}
	return u.String(), nil
}


func (c *Client) BucketExists(ctx context.Context) (bool, error) {
    exists, err := c.Client.BucketExists(ctx, c.Bucket)
    if err != nil {
        return false, fmt.Errorf("failed to check bucket existence: %w", err)
    }
    log.Println("Bucket exists:", exists)
    return exists, nil
}

func (c *Client) Delete(ctx context.Context, objectName string) error {
    return c.Client.RemoveObject(ctx, c.Bucket, objectName, minio.RemoveObjectOptions{})
}

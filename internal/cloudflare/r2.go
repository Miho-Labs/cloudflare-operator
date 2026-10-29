package cloudflare

import (
	"context"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/r2"
)

// R2Bucket is the operator's view of an R2 bucket.
type R2Bucket struct {
	Name         string
	Location     string
	StorageClass string
	Jurisdiction string
}

// R2BucketInput describes a bucket to create.
type R2BucketInput struct {
	Name         string
	LocationHint string
	StorageClass string
	Jurisdiction string
}

// R2Endpoint returns the S3-compatible endpoint of the account, honouring the jurisdiction.
func (c *Client) R2Endpoint(jurisdiction string) string {
	switch jurisdiction {
	case "", "default":
		return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", c.AccountID)
	default:
		return fmt.Sprintf("https://%s.%s.r2.cloudflarestorage.com", c.AccountID, jurisdiction)
	}
}

// GetR2Bucket fetches a bucket. Returns nil when it does not exist.
func (c *Client) GetR2Bucket(ctx context.Context, name, jurisdiction string) (*R2Bucket, error) {
	params := r2.BucketGetParams{AccountID: cf.F(c.AccountID)}
	if jurisdiction != "" {
		params.CfR2Jurisdiction = cf.F(r2.BucketGetParamsCfR2Jurisdiction(jurisdiction))
	}
	res, err := c.api.R2.Buckets.Get(ctx, name, params)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get r2 bucket %s: %w", name, err)
	}
	return &R2Bucket{Name: res.Name, Location: string(res.Location), StorageClass: string(res.StorageClass), Jurisdiction: string(res.Jurisdiction)}, nil
}

// CreateR2Bucket creates a bucket.
func (c *Client) CreateR2Bucket(ctx context.Context, in R2BucketInput) (*R2Bucket, error) {
	params := r2.BucketNewParams{AccountID: cf.F(c.AccountID), Name: cf.F(in.Name)}
	if in.LocationHint != "" {
		params.LocationHint = cf.F(r2.BucketNewParamsLocationHint(in.LocationHint))
	}
	if in.StorageClass != "" {
		params.StorageClass = cf.F(r2.BucketNewParamsStorageClass(in.StorageClass))
	}
	if in.Jurisdiction != "" {
		params.CfR2Jurisdiction = cf.F(r2.BucketNewParamsCfR2Jurisdiction(in.Jurisdiction))
	}
	res, err := c.api.R2.Buckets.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create r2 bucket %s: %w", in.Name, err)
	}
	return &R2Bucket{Name: res.Name, Location: string(res.Location), StorageClass: string(res.StorageClass), Jurisdiction: string(res.Jurisdiction)}, nil
}

// DeleteR2Bucket deletes a bucket. A missing bucket is not an error; a non-empty bucket is.
func (c *Client) DeleteR2Bucket(ctx context.Context, name, jurisdiction string) error {
	params := r2.BucketDeleteParams{AccountID: cf.F(c.AccountID)}
	if jurisdiction != "" {
		params.CfR2Jurisdiction = cf.F(r2.BucketDeleteParamsCfR2Jurisdiction(jurisdiction))
	}
	_, err := c.api.R2.Buckets.Delete(ctx, name, params)
	if err != nil && !IsNotFound(err) {
		return fmt.Errorf("delete r2 bucket %s: %w", name, err)
	}
	return nil
}

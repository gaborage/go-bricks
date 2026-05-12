// Package awssm implements a migration.SecretFetcher backed by AWS Secrets Manager.
package awssm

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gaborage/go-bricks/migration"
)

// Options configures the AWS Secrets Manager client built by NewFetcher.
type Options struct {
	// Region overrides the default AWS region. Empty falls back to the
	// SDK's default chain (AWS_REGION env var, ~/.aws/config, etc.).
	Region string

	// Profile selects a named profile from the shared config files.
	// Empty falls back to AWS_PROFILE / "default".
	Profile string

	// Endpoint overrides the AWS Secrets Manager endpoint. Used for
	// LocalStack / private VPC endpoints. Empty for production.
	Endpoint string
}

// SecretValueGetter is the subset of the AWS SDK we depend on. Defined as an
// interface so tests can substitute a fake without spinning up an HTTP server.
type SecretValueGetter interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// NewFetcher builds a migration.SecretFetcher that calls AWS Secrets Manager.
// The fetcher follows the standard AWS credential chain (IRSA, instance
// profile, shared config, env vars) by default.
func NewFetcher(ctx context.Context, opts Options) (migration.SecretFetcher, error) {
	cfgOpts := []func(*awsconfig.LoadOptions) error{}
	if opts.Region != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithRegion(opts.Region))
	}
	if opts.Profile != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithSharedConfigProfile(opts.Profile))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, cfgOpts...)
	if err != nil {
		return nil, fmt.Errorf("awssm: load AWS config: %w", err)
	}

	smOpts := []func(*secretsmanager.Options){}
	if opts.Endpoint != "" {
		endpoint := opts.Endpoint
		smOpts = append(smOpts, func(o *secretsmanager.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}

	client := secretsmanager.NewFromConfig(awsCfg, smOpts...)
	return FetcherFromClient(client), nil
}

// FetcherFromClient wraps any SecretValueGetter as a migration.SecretFetcher.
// Exposed primarily for tests but also useful when callers already hold a
// configured client. A nil client returns a fetcher that fails deterministically
// rather than panicking on a nil-interface method call.
func FetcherFromClient(client SecretValueGetter) migration.SecretFetcher {
	if client == nil {
		return func(context.Context, string) ([]byte, error) {
			return nil, errors.New("awssm: nil SecretValueGetter client")
		}
	}
	return func(ctx context.Context, name string) ([]byte, error) {
		out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
			SecretId: aws.String(name),
		})
		if err != nil {
			return nil, fmt.Errorf("awssm: GetSecretValue %q: %w", name, err)
		}
		if out == nil {
			return nil, errors.New("awssm: nil GetSecretValue response")
		}
		if out.SecretString != nil {
			return []byte(*out.SecretString), nil
		}
		if len(out.SecretBinary) > 0 {
			return out.SecretBinary, nil
		}
		return nil, fmt.Errorf("awssm: secret %q has neither SecretString nor SecretBinary", name)
	}
}

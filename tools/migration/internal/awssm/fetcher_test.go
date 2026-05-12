package awssm

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSM struct {
	captured *secretsmanager.GetSecretValueInput
	out      *secretsmanager.GetSecretValueOutput
	err      error
}

func (f *fakeSM) GetSecretValue(_ context.Context, params *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.captured = params
	return f.out, f.err
}

func TestFetcherFromClientReturnsSecretString(t *testing.T) {
	client := &fakeSM{
		out: &secretsmanager.GetSecretValueOutput{
			SecretString: aws.String(`{"type":"postgresql"}`),
		},
	}

	fetcher := FetcherFromClient(client)
	got, err := fetcher(context.Background(), "gobricks/migrate/tenant-a")
	require.NoError(t, err)
	assert.Equal(t, `{"type":"postgresql"}`, string(got))
	require.NotNil(t, client.captured)
	assert.Equal(t, "gobricks/migrate/tenant-a", aws.ToString(client.captured.SecretId))
}

func TestFetcherFromClientReturnsSecretBinary(t *testing.T) {
	client := &fakeSM{
		out: &secretsmanager.GetSecretValueOutput{
			SecretBinary: []byte(`{"type":"oracle"}`),
		},
	}

	fetcher := FetcherFromClient(client)
	got, err := fetcher(context.Background(), "gobricks/migrate/tenant-b")
	require.NoError(t, err)
	assert.Equal(t, `{"type":"oracle"}`, string(got))
}

func TestFetcherFromClientPropagatesError(t *testing.T) {
	boom := errors.New("access denied")
	client := &fakeSM{err: boom}

	fetcher := FetcherFromClient(client)
	_, err := fetcher(context.Background(), "gobricks/migrate/tenant-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}

func TestFetcherFromClientRejectsEmptyResponse(t *testing.T) {
	client := &fakeSM{out: &secretsmanager.GetSecretValueOutput{}}

	fetcher := FetcherFromClient(client)
	_, err := fetcher(context.Background(), "gobricks/migrate/tenant-a")
	assert.Error(t, err)
}

func TestFetcherFromClientRejectsNilResponse(t *testing.T) {
	client := &fakeSM{}

	fetcher := FetcherFromClient(client)
	_, err := fetcher(context.Background(), "gobricks/migrate/tenant-a")
	assert.Error(t, err)
}

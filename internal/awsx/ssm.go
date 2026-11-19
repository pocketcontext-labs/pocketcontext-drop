package awsx

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// SSMAPI is the subset of the SSM client the secret store calls.
type SSMAPI interface {
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, opts ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, opts ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// SSMSecretStore keeps secrets as standard-tier SecureString parameters
// encrypted with the account's AWS managed key (alias/aws/ssm). Errors name
// the parameter, never the value.
type SSMSecretStore struct {
	api SSMAPI
}

// NewSSMSecretStore wraps an SSM client.
func NewSSMSecretStore(api SSMAPI) *SSMSecretStore { return &SSMSecretStore{api: api} }

func (s *SSMSecretStore) Put(ctx context.Context, name, value string) (int64, error) {
	if err := ValidateParameterName(name); err != nil {
		return 0, err
	}
	if err := checkSecretValue(value); err != nil {
		return 0, err
	}
	out, err := s.api.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String(value),
		Type:      types.ParameterTypeSecureString,
		Tier:      types.ParameterTierStandard,
		Overwrite: aws.Bool(true),
	})
	if err != nil {
		return 0, fmt.Errorf("awsx: SSM put %s: %w", name, err)
	}
	return out.Version, nil
}

func (s *SSMSecretStore) Delete(ctx context.Context, name string) error {
	if err := ValidateParameterName(name); err != nil {
		return err
	}
	_, err := s.api.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)})
	var nf *types.ParameterNotFound
	if err != nil && !errors.As(err, &nf) {
		return fmt.Errorf("awsx: SSM delete %s: %w", name, err)
	}
	return nil
}

func (s *SSMSecretStore) Get(ctx context.Context, name string, version int64) (string, error) {
	if err := ValidateParameterName(name); err != nil {
		return "", err
	}
	if err := checkVersion(version); err != nil {
		return "", err
	}
	out, err := s.api.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name + ":" + strconv.FormatInt(version, 10)),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var nf *types.ParameterNotFound
		var vnf *types.ParameterVersionNotFound
		if errors.As(err, &nf) || errors.As(err, &vnf) {
			return "", fmt.Errorf("%w: %s version %d", ErrNotFound, name, version)
		}
		return "", fmt.Errorf("awsx: SSM get %s version %d: %w", name, version, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", fmt.Errorf("awsx: SSM get %s version %d: empty response", name, version)
	}
	return *out.Parameter.Value, nil
}

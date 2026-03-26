package cli

import (
	"errors"
	"fmt"

	apiurl "github.com/entireio/cli/cmd/entire/cli/api"
	"github.com/entireio/cli/cmd/entire/cli/auth"
)

// NewAuthenticatedAPIClient creates an API client using the bearer token
// from the CLI login flow. Returns an error if the user is not logged in.
func NewAuthenticatedAPIClient() (*apiurl.Client, error) {
	token, err := auth.LookupCurrentToken()
	if err != nil {
		return nil, fmt.Errorf("lookup auth token: %w", err)
	}
	if token == "" {
		return nil, errors.New("not logged in (run 'entire login' first)")
	}

  if err := apiurl.RequireSecureURL(apiurl.BaseURL()); err != nil {
    return nil, err
  }
	return apiurl.NewClient(token), nil
}

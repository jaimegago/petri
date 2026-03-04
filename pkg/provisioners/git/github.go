package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// createRepoRequest carries parameters for the GitHub create-repository endpoint.
type createRepoRequest struct {
	Owner       string
	Name        string
	Description string
	Private     bool
	// IsOrg indicates that Owner is a GitHub organisation.
	// When true the POST goes to /orgs/{Owner}/repos instead of /user/repos.
	IsOrg bool
}

// apiClient implements gitHubClient against the real GitHub REST API.
type apiClient struct {
	token   string
	baseURL string
	http    *http.Client
}

// newAPIClient constructs an apiClient.
// baseURL should not have a trailing slash.
func newAPIClient(token, baseURL string) *apiClient {
	return &apiClient{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// createRepo calls POST /user/repos or POST /orgs/{org}/repos.
func (c *apiClient) createRepo(ctx context.Context, req createRepoRequest) (*RepoInfo, error) {
	endpoint := c.baseURL + "/user/repos"
	if req.IsOrg && req.Owner != "" {
		endpoint = fmt.Sprintf("%s/orgs/%s/repos", c.baseURL, req.Owner)
	}

	payload, err := json.Marshal(map[string]any{
		"name":        req.Name,
		"description": req.Description,
		"private":     req.Private,
		"auto_init":   false,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling create-repo payload: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building create-repo request: %w", err)
	}
	c.setHeaders(httpReq)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if err := c.checkRateLimit(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, parseAPIError(resp, "creating repository")
	}

	var result struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		SSHURL        string `json:"ssh_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding create-repo response: %w", err)
	}

	return &RepoInfo{
		Name:          result.Name,
		FullName:      result.FullName,
		CloneURL:      result.CloneURL,
		SSHURL:        result.SSHURL,
		DefaultBranch: result.DefaultBranch,
	}, nil
}

// deleteRepo calls DELETE /repos/{owner}/{name}.
func (c *apiClient) deleteRepo(ctx context.Context, owner, name string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, owner, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building delete-repo request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", endpoint, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if err := c.checkRateLimit(resp); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent {
		return parseAPIError(resp, "deleting repository")
	}
	return nil
}

// setHeaders adds authentication and content-type headers.
func (c *apiClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// checkRateLimit returns an error when the GitHub API rate limit is exhausted.
func (c *apiClient) checkRateLimit(resp *http.Response) error {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return nil
	}
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			resetAt := time.Unix(epoch, 0).UTC()
			return fmt.Errorf("GitHub API rate limit exceeded; resets at %s", resetAt.Format(time.RFC3339))
		}
	}
	return fmt.Errorf("GitHub API rate limit exceeded")
}

// parseAPIError decodes a GitHub error response into a Go error.
func parseAPIError(resp *http.Response, op string) error {
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Message == "" {
		return fmt.Errorf("%s: HTTP %d", op, resp.StatusCode)
	}
	if len(body.Errors) > 0 {
		e := body.Errors[0]
		return fmt.Errorf("%s: %s (%s: %s)", op, body.Message, e.Field, e.Message)
	}
	return fmt.Errorf("%s: %s", op, body.Message)
}

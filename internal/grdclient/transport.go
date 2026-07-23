package grdclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

type remote struct {
	baseURL  string
	repoID   string
	username string
	password string
}

type intentResponse struct {
	ID         string `json:"id"`
	ContentRef struct {
		Engine   string `json:"engine"`
		Revision string `json:"revision"`
	} `json:"contentRef"`
}

type proposalResponse struct {
	Change struct {
		ID string `json:"id"`
	} `json:"change"`
	Version struct {
		ID           string   `json:"id"`
		Dependencies []string `json:"dependencies"`
	} `json:"version"`
	Promotion *struct {
		ID       string `json:"id"`
		ToIntent string `json:"toIntent"`
	} `json:"promotion"`
}

type changeResponse struct {
	ID            string `json:"id"`
	LatestVersion struct {
		ID         string `json:"id"`
		BaseIntent string `json:"baseIntent"`
		ContentRef struct {
			Engine   string `json:"engine"`
			Revision string `json:"revision"`
		} `json:"contentRef"`
	} `json:"latestVersion"`
	LatestAmendment *struct {
		FromVersion string `json:"fromVersion"`
		ToVersion   string `json:"toVersion"`
		Rationale   string `json:"rationale"`
	} `json:"latestAmendment"`
	LatestPromotion *struct {
		ToIntent string `json:"toIntent"`
		Version  string `json:"version"`
	} `json:"latestPromotion"`
}

type reconciliationConflictResponse struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Change struct {
		ID string `json:"id"`
	} `json:"change"`
	Version struct {
		ID           string   `json:"id"`
		Change       string   `json:"change"`
		BaseIntent   string   `json:"baseIntent"`
		Producer     string   `json:"producer"`
		Dependencies []string `json:"dependencies"`
		ContentRef   struct {
			Engine   string `json:"engine"`
			Revision string `json:"revision"`
		} `json:"contentRef"`
	} `json:"version"`
	FromVersion   string   `json:"fromVersion"`
	ToVersion     string   `json:"toVersion"`
	ReportedBy    string   `json:"reportedBy"`
	AffectedPaths []string `json:"affectedPaths"`
}

func requireAncestor(ctx context.Context, workdir string, base string, head string) error {
	ancestor, err := isAncestor(ctx, workdir, base, head)
	if err != nil {
		return err
	}
	if ancestor {
		return nil
	}
	return errors.New("workspace is not based on current intent; sync before submitting")
}

func isAncestor(ctx context.Context, workdir string, base string, head string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", base, head)
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	err := command.Run()
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check workspace ancestry: %w", err)
}

func discoverRemote(ctx context.Context, workdir string) (remote, error) {
	raw, err := gitOutput(ctx, workdir, "remote", "get-url", "--push", "origin")
	if err != nil {
		return remote{}, fmt.Errorf("read origin: %w", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return remote{}, errors.New("origin must be a gitrdone HTTP remote")
	}
	const marker = "/git/repos/"
	markerIndex := strings.Index(parsed.EscapedPath(), marker)
	if markerIndex < 0 {
		return remote{}, errors.New("origin is not a gitrdone repository URL")
	}
	repoPath := parsed.EscapedPath()[markerIndex+len(marker):]
	repoID := strings.TrimSuffix(repoPath, ".git")
	if repoID == "" || strings.Contains(repoID, "/") {
		return remote{}, errors.New("origin contains an invalid gitrdone repository id")
	}

	username, password, err := gitCredential(ctx, workdir, raw)
	if err != nil {
		return remote{}, err
	}
	return remote{
		baseURL:  parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()[:markerIndex],
		repoID:   repoID,
		username: username,
		password: password,
	}, nil
}

func gitCredential(ctx context.Context, workdir string, remoteURL string) (string, string, error) {
	command := exec.CommandContext(ctx, "git", "credential", "fill")
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader("url=" + remoteURL + "\n\n")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", "", errors.New("origin credential is unavailable")
	}
	values := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["password"] == "" {
		return "", "", errors.New("origin credential is unavailable")
	}
	username := values["username"]
	if username == "" {
		username = "x-access-token"
	}
	return username, values["password"], nil
}

func (client Client) currentIntent(ctx context.Context, remote remote) (intentResponse, error) {
	var current intentResponse
	if err := client.getJSON(ctx, remote, "/v1/repos/"+remote.repoID+"/intent", &current); err != nil {
		return intentResponse{}, fmt.Errorf("read current intent: %w", err)
	}
	if current.ID == "" {
		return intentResponse{}, errors.New("read current intent: response has no intent id")
	}
	return current, nil
}

func (client Client) change(ctx context.Context, remote remote, changeID string) (changeResponse, error) {
	var change changeResponse
	if err := client.getJSON(ctx, remote, "/v1/repos/"+remote.repoID+"/changes/"+changeID, &change); err != nil {
		return changeResponse{}, fmt.Errorf("inspect submitted change: %w", err)
	}
	if change.ID == "" || change.LatestVersion.ID == "" {
		return changeResponse{}, errors.New("inspect submitted change: response has no change version")
	}
	return change, nil
}

func (client Client) propose(ctx context.Context, remote remote, baseIntent string, revision string, dependencies []string, idempotencyKey string) (proposalResponse, error) {
	body := map[string]any{
		"baseIntent": baseIntent,
		"contentRef": map[string]string{
			"engine":   "git",
			"revision": revision,
		},
	}
	if len(dependencies) > 0 {
		body["dependencies"] = dependencies
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return proposalResponse{}, fmt.Errorf("encode proposal: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, remote.baseURL+"/v1/repos/"+remote.repoID+"/proposals", bytes.NewReader(encoded))
	if err != nil {
		return proposalResponse{}, fmt.Errorf("create proposal request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.SetBasicAuth(remote.username, remote.password)
	var receipt proposalResponse
	if err := client.doJSON(request, &receipt); err != nil {
		return proposalResponse{}, fmt.Errorf("propose content: %w", err)
	}
	return receipt, nil
}

func (client Client) recordReconciliationConflict(
	ctx context.Context,
	remote remote,
	fromVersion string,
	toVersion string,
	descendantVersion string,
	affectedPaths []string,
) (reconciliationConflictResponse, error) {
	body := map[string]any{
		"fromVersion":       fromVersion,
		"toVersion":         toVersion,
		"descendantVersion": descendantVersion,
	}
	if len(affectedPaths) > 0 {
		body["affectedPaths"] = affectedPaths
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return reconciliationConflictResponse{}, fmt.Errorf("encode reconciliation conflict: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, remote.baseURL+"/v1/repos/"+remote.repoID+"/reconciliation-conflicts", bytes.NewReader(encoded))
	if err != nil {
		return reconciliationConflictResponse{}, fmt.Errorf("create reconciliation conflict request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", reconciliationConflictIdempotencyKey(remote.repoID, fromVersion, toVersion, descendantVersion))
	request.SetBasicAuth(remote.username, remote.password)
	var conflict reconciliationConflictResponse
	if err := client.doJSON(request, &conflict); err != nil {
		return reconciliationConflictResponse{}, fmt.Errorf("record reconciliation conflict: %w", err)
	}
	return conflict, nil
}

func (client Client) reconciliationConflict(ctx context.Context, remote remote, conflictID string) (reconciliationConflictResponse, error) {
	var conflict reconciliationConflictResponse
	if err := client.getJSON(ctx, remote, "/v1/repos/"+remote.repoID+"/reconciliation-conflicts/"+conflictID, &conflict); err != nil {
		return reconciliationConflictResponse{}, fmt.Errorf("inspect reconciliation conflict: %w", err)
	}
	return conflict, nil
}

func (client Client) getJSON(ctx context.Context, remote remote, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remote.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.SetBasicAuth(remote.username, remote.password)
	return client.doJSON(request, target)
}

func (client Client) doJSON(request *http.Request, target any) error {
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return fmt.Errorf("server returned %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64*1024))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}

func gitOutput(ctx context.Context, workdir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitRun(ctx context.Context, workdir string, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

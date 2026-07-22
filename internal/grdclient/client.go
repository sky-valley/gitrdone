package grdclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

type Client struct {
	HTTPClient *http.Client
	Stdout     io.Writer
}

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
	Promotion *struct {
		ID string `json:"id"`
	} `json:"promotion"`
}

type submissionState struct {
	BaseIntent     string `json:"baseIntent"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func (client Client) Submit(ctx context.Context, workdir string) error {
	if client.Stdout == nil {
		client.Stdout = io.Discard
	}
	if client.HTTPClient == nil {
		client.HTTPClient = http.DefaultClient
	}

	status, err := gitOutput(ctx, workdir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("read workspace status: %w", err)
	}
	if status != "" {
		return errors.New("workspace has uncommitted changes; commit them before submitting")
	}

	origin, err := discoverRemote(ctx, workdir)
	if err != nil {
		return err
	}
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read workspace revision: %w", err)
	}
	title, err := gitOutput(ctx, workdir, "log", "-1", "--format=%s", "HEAD")
	if err != nil {
		return fmt.Errorf("read change title: %w", err)
	}
	state, found, err := loadSubmissionState(ctx, workdir, origin.repoID, head)
	if err != nil {
		return err
	}
	if !found {
		current, err := client.currentIntent(ctx, origin)
		if err != nil {
			return err
		}
		if current.ContentRef.Engine != "git" || current.ContentRef.Revision == "" {
			return errors.New("current intent is not represented by Git content")
		}
		if current.ContentRef.Revision == head {
			return errors.New("workspace has no new committed content")
		}
		if err := requireAncestor(ctx, workdir, current.ContentRef.Revision, head); err != nil {
			return err
		}
		idempotencyKey, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		state = submissionState{BaseIntent: current.ID, IdempotencyKey: idempotencyKey}
		if err := rememberSubmissionState(ctx, workdir, origin.repoID, head, state); err != nil {
			return err
		}
	}

	candidateRef := "refs/candidates/grd/" + head
	if err := gitRun(ctx, workdir, "push", "origin", "HEAD:"+candidateRef); err != nil {
		return fmt.Errorf("publish proposed content: %w", err)
	}
	receipt, err := client.propose(ctx, origin, state.BaseIntent, head, state.IdempotencyKey)
	if err != nil {
		return err
	}

	fmt.Fprintf(client.Stdout, "Submitted: %s\n", title)
	if receipt.Promotion == nil {
		fmt.Fprintln(client.Stdout, "Judgement in progress")
		fmt.Fprintln(client.Stdout, "You can keep working.")
		return nil
	}
	fmt.Fprintln(client.Stdout, "Promoted")
	fmt.Fprintln(client.Stdout, "You can keep working.")
	return nil
}

func loadSubmissionState(ctx context.Context, workdir string, repoID string, revision string) (submissionState, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", submissionStateKey(repoID, revision))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var state submissionState
		if json.Unmarshal(bytes.TrimSpace(output), &state) != nil || state.BaseIntent == "" || state.IdempotencyKey == "" {
			return submissionState{}, false, errors.New("stored submission retry state is invalid")
		}
		return state, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return submissionState{}, false, nil
	}
	return submissionState{}, false, fmt.Errorf("read submission retry state: %w", err)
}

func rememberSubmissionState(ctx context.Context, workdir string, repoID string, revision string, state submissionState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode submission retry state: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", submissionStateKey(repoID, revision), string(encoded)); err != nil {
		return fmt.Errorf("store submission retry state: %w", err)
	}
	return nil
}

func submissionStateKey(repoID string, revision string) string {
	digest := sha256.Sum256([]byte(repoID + "\x00" + revision))
	return "grd-submission." + hex.EncodeToString(digest[:]) + ".state"
}

func newIdempotencyKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create submission idempotency key: %w", err)
	}
	return "grd-submit-" + hex.EncodeToString(raw[:]), nil
}

func requireAncestor(ctx context.Context, workdir string, base string, head string) error {
	command := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", base, head)
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	err := command.Run()
	if err == nil {
		return nil
	}
	return errors.New("workspace is not based on current intent; sync before submitting")
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

func (client Client) propose(ctx context.Context, remote remote, baseIntent string, revision string, idempotencyKey string) (proposalResponse, error) {
	body, err := json.Marshal(map[string]any{
		"baseIntent": baseIntent,
		"contentRef": map[string]string{
			"engine":   "git",
			"revision": revision,
		},
	})
	if err != nil {
		return proposalResponse{}, fmt.Errorf("encode proposal: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, remote.baseURL+"/v1/repos/"+remote.repoID+"/proposals", bytes.NewReader(body))
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

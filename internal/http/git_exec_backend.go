package httpapi

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxGitBackendStderrBytes = 64 * 1024

type execGitHTTPBackend struct {
	gitPath string
}

func (backend execGitHTTPBackend) ServeHTTPGit(w http.ResponseWriter, r *http.Request, grant gitAccessGrant) error {
	gitPath := strings.TrimSpace(backend.gitPath)
	if gitPath == "" {
		gitPath = "git"
	}

	cmd := exec.CommandContext(r.Context(), gitPath, "http-backend")
	cmd.Env = gitHTTPBackendEnv(r, grant)
	cmd.Stdin = r.Body

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open git http-backend stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open git http-backend stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start git http-backend: %w", err)
	}

	stderrDone := make(chan string, 1)
	go func() {
		output, _ := io.ReadAll(io.LimitReader(stderr, maxGitBackendStderrBytes))
		stderrDone <- strings.TrimSpace(string(output))
	}()

	wroteResponse, err := copyGitBackendResponse(w, stdout)
	waitErr := cmd.Wait()
	stderrText := <-stderrDone
	if err != nil {
		return err
	}
	if waitErr != nil && !wroteResponse {
		if stderrText != "" {
			return fmt.Errorf("git http-backend failed: %w: %s", waitErr, stderrText)
		}
		return fmt.Errorf("git http-backend failed: %w", waitErr)
	}
	return nil
}

func gitHTTPBackendEnv(r *http.Request, grant gitAccessGrant) []string {
	env := gitProcessEnv(
		"GIT_PROJECT_ROOT="+filepath.Dir(grant.RepoPath),
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=/"+filepath.Base(grant.RepoPath)+"/"+r.PathValue("gitPath"),
		"QUERY_STRING="+r.URL.RawQuery,
		"REQUEST_METHOD="+r.Method,
		"REMOTE_USER="+gitRemoteUser(grant),
	)
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		env = append(env, "CONTENT_TYPE="+contentType)
	}
	if r.ContentLength >= 0 {
		env = append(env, "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
	}
	if protocol := r.Header.Get("Git-Protocol"); protocol != "" {
		env = append(env, "GIT_PROTOCOL="+protocol)
	}
	return env
}

func gitRemoteUser(grant gitAccessGrant) string {
	if grant.Subject != "" {
		return grant.Subject
	}
	return "gitrdone"
}

func copyGitBackendResponse(w http.ResponseWriter, stdout io.Reader) (bool, error) {
	reader := bufio.NewReader(stdout)
	status := http.StatusOK
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, errors.New("git http-backend returned no complete response headers")
			}
			return false, fmt.Errorf("read git http-backend response header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return false, fmt.Errorf("invalid git http-backend response header %q", line)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			parsedStatus, err := parseGitBackendStatus(value)
			if err != nil {
				return false, err
			}
			status = parsedStatus
			continue
		}
		w.Header().Add(name, value)
	}

	w.WriteHeader(status)
	if _, err := io.Copy(w, reader); err != nil {
		return true, fmt.Errorf("copy git http-backend response body: %w", err)
	}
	return true, nil
}

func parseGitBackendStatus(value string) (int, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, errors.New("empty git http-backend status header")
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil || status < 100 || status > 999 {
		return 0, fmt.Errorf("invalid git http-backend status header %q", value)
	}
	return status, nil
}

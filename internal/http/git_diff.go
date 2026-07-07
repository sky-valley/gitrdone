package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

// Read-only diff endpoints layered over the git object store. A caller with a
// read-scoped repo token can fetch a single commit's patch
// (`GET /git/repos/{repoGitID}/show/{sha}.diff`) or an endpoint/merge-base diff
// between two commits (`GET /git/repos/{repoGitID}/compare/{base}..{head}.diff`
// or `...`), without cloning. Every revision is a validated lowercase hex object
// id, so it can never be interpreted as a git flag or path.

const (
	// A diff body is buffered under this cap so a genuine git failure still maps
	// to a clean 500 (we only write 200 once the command has fully succeeded).
	maxGitDiffBytes = 8 * 1024 * 1024
	// git stderr is bounded to keep an error message useful but not unbounded.
	maxGitDiffStderrBytes = 64 * 1024
	// Object ids may be abbreviated; git resolves them (ambiguous/unknown ids
	// fall through to the not-found path).
	minGitObjectIDLength = 7
	maxGitObjectIDLength = 64
)

var errGitDiffTooLarge = errors.New("git diff output exceeded size limit")
var errGitRevisionNotFound = errors.New("git revision not found")
var errGitDiffNoMergeBase = errors.New("git diff revisions do not share a merge base")

type gitDiffKind int

const (
	gitDiffShow gitDiffKind = iota
	gitDiffCompare
)

// gitDiffTarget is the validated description of what to diff. revspec is the
// single argument handed to git — a lone object id for show, or "base..head" /
// "base...head" for compare. revs lists the object ids to pre-verify so an
// unknown id becomes a 404 before any response body is streamed.
type gitDiffTarget struct {
	kind    gitDiffKind
	revspec string
	revs    []string
}

type gitDiffBackend interface {
	ServeGitDiff(w http.ResponseWriter, r *http.Request, grant gitAccessGrant, target gitDiffTarget) error
}

type notImplementedGitDiffBackend struct{}

func (notImplementedGitDiffBackend) ServeGitDiff(w http.ResponseWriter, r *http.Request, grant gitAccessGrant, target gitDiffTarget) error {
	http.Error(w, "git diff backend is not implemented", http.StatusNotImplemented)
	return nil
}

func gitDiffHandler(access gitAccessAuthorizer, backend gitDiffBackend, kind gitDiffKind) http.Handler {
	if backend == nil {
		backend = notImplementedGitDiffBackend{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := parseRepoControlID(r.PathValue("repoID"))
		if !ok {
			http.Error(w, "repo id is invalid", http.StatusBadRequest)
			return
		}
		target, ok := parseGitDiffSpec(kind, r.PathValue("spec"))
		if !ok {
			http.Error(w, "diff revision is invalid", http.StatusBadRequest)
			return
		}

		grant, err := access.AuthorizeGitAccess(r.Context(), authorizeGitAccessInput{
			RepoID:    repoID,
			Token:     gitTokenFromRequest(r),
			Operation: gitOperationRead,
		})
		if err != nil {
			writeGitAccessError(w, err)
			return
		}

		if err := backend.ServeGitDiff(w, r, grant, target); err != nil {
			if errors.Is(err, errGitDiffTooLarge) {
				http.Error(w, "git diff is too large", http.StatusRequestEntityTooLarge)
				return
			}
			if errors.Is(err, errGitDiffNoMergeBase) {
				http.Error(w, "diff revisions do not share a merge base", http.StatusConflict)
				return
			}
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	})
}

func parseGitDiffSpec(kind gitDiffKind, raw string) (gitDiffTarget, bool) {
	spec, ok := strings.CutSuffix(strings.TrimSpace(raw), ".diff")
	if !ok || spec == "" {
		return gitDiffTarget{}, false
	}

	switch kind {
	case gitDiffShow:
		if !isGitObjectID(spec) {
			return gitDiffTarget{}, false
		}
		return gitDiffTarget{kind: kind, revspec: spec, revs: []string{spec}}, true
	case gitDiffCompare:
		separator := ".."
		if strings.Contains(spec, "...") {
			separator = "..."
		}
		base, head, ok := strings.Cut(spec, separator)
		if !ok || !isGitObjectID(base) || !isGitObjectID(head) {
			return gitDiffTarget{}, false
		}
		return gitDiffTarget{kind: kind, revspec: base + separator + head, revs: []string{base, head}}, true
	default:
		return gitDiffTarget{}, false
	}
}

// isGitObjectID reports whether value is a lowercase hex object id in the
// abbreviated..full range. Hex-only also guarantees no leading dash, so a
// validated id can never be parsed by git as an option.
func isGitObjectID(value string) bool {
	if len(value) < minGitObjectIDLength || len(value) > maxGitObjectIDLength {
		return false
	}
	for _, char := range value {
		if !isLowerHexDigit(char) {
			return false
		}
	}
	return true
}

type execGitDiffBackend struct {
	gitPath string
}

func (backend execGitDiffBackend) ServeGitDiff(w http.ResponseWriter, r *http.Request, grant gitAccessGrant, target gitDiffTarget) error {
	gitPath := strings.TrimSpace(backend.gitPath)
	if gitPath == "" {
		gitPath = "git"
	}
	ctx := r.Context()

	resolvedTarget, err := resolveGitDiffTarget(ctx, gitPath, grant.RepoPath, target)
	if err != nil {
		if errors.Is(err, errGitRevisionNotFound) {
			http.Error(w, "diff revision was not found", http.StatusNotFound)
			return nil
		}
		return err
	}

	patch, err := runGitDiff(ctx, gitPath, gitDiffArgs(grant.RepoPath, resolvedTarget))
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(patch)
	return nil
}

func gitDiffArgs(repoPath string, target gitDiffTarget) []string {
	// --no-color/--no-ext-diff/--no-textconv/--full-index keep the output a
	// plain, portable, git-apply-able patch and block external diff/textconv
	// drivers a crafted repo tree could otherwise invoke.
	common := []string{"--no-color", "--no-ext-diff", "--no-textconv", "--full-index"}
	switch target.kind {
	case gitDiffShow:
		args := []string{"--git-dir", repoPath, "show", "--patch", "--format=", "--diff-merges=first-parent"}
		args = append(args, common...)
		return append(args, target.revspec, "--")
	default:
		args := []string{"--git-dir", repoPath, "diff"}
		args = append(args, common...)
		return append(args, target.revspec, "--")
	}
}

func resolveGitDiffTarget(ctx context.Context, gitPath string, repoPath string, target gitDiffTarget) (gitDiffTarget, error) {
	resolvedRevs := make([]string, 0, len(target.revs))
	for _, rev := range target.revs {
		resolved, err := resolveGitCommit(ctx, gitPath, repoPath, rev)
		if err != nil {
			return gitDiffTarget{}, err
		}
		resolvedRevs = append(resolvedRevs, resolved)
	}

	switch target.kind {
	case gitDiffShow:
		return gitDiffTarget{kind: target.kind, revspec: resolvedRevs[0], revs: resolvedRevs}, nil
	case gitDiffCompare:
		separator := ".."
		if strings.Contains(target.revspec, "...") {
			separator = "..."
		}
		return gitDiffTarget{kind: target.kind, revspec: resolvedRevs[0] + separator + resolvedRevs[1], revs: resolvedRevs}, nil
	default:
		return gitDiffTarget{}, fmt.Errorf("unknown git diff kind %d", target.kind)
	}
}

func resolveGitCommit(ctx context.Context, gitPath string, repoPath string, rev string) (string, error) {
	stdout := &bytes.Buffer{}
	stderr := &cappedBuffer{limit: maxGitDiffStderrBytes}
	cmd := exec.CommandContext(ctx, gitPath, "--git-dir", repoPath, "rev-parse", "--verify", rev+"^{commit}")
	cmd.Env = gitProcessEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		message := strings.TrimSpace(stderr.buf.String())
		if isMissingGitRevisionError(err, message) {
			return "", errGitRevisionNotFound
		}
		if message != "" {
			return "", fmt.Errorf("resolve git revision: %w: %s", err, message)
		}
		return "", fmt.Errorf("resolve git revision: %w", err)
	}

	commit := strings.TrimSpace(stdout.String())
	if !isGitObjectID(commit) {
		return "", fmt.Errorf("resolve git revision returned invalid object id %q", commit)
	}
	return commit, nil
}

func isMissingGitRevisionError(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	message := strings.ToLower(stderr)
	if strings.Contains(message, "not a git repository") {
		return false
	}
	return strings.Contains(message, "needed a single revision") ||
		strings.Contains(message, "not a valid object name") ||
		strings.Contains(message, "expected commit type") ||
		strings.Contains(message, "ambiguous")
}

func runGitDiff(ctx context.Context, gitPath string, args []string) ([]byte, error) {
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stderr := &cappedBuffer{limit: maxGitDiffStderrBytes}

	cmd := exec.CommandContext(cmdCtx, gitPath, args...)
	cmd.Env = gitProcessEnv()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open git diff stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open git diff stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start git diff: %w", err)
	}

	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, stderrPipe)
		close(stderrDone)
	}()

	patch, readErr := io.ReadAll(io.LimitReader(stdout, int64(maxGitDiffBytes)+1))
	tooLarge := len(patch) > maxGitDiffBytes
	if tooLarge {
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	waitErr := cmd.Wait()
	<-stderrDone

	if tooLarge {
		return nil, errGitDiffTooLarge
	}
	if readErr != nil {
		return nil, fmt.Errorf("read git diff output: %w", readErr)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.buf.String())
		if isNoMergeBaseGitDiffError(waitErr, message) {
			return nil, errGitDiffNoMergeBase
		}
		if message != "" {
			return nil, fmt.Errorf("git diff failed: %w: %s", waitErr, message)
		}
		return nil, fmt.Errorf("git diff failed: %w", waitErr)
	}
	return patch, nil
}

func isNoMergeBaseGitDiffError(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return strings.Contains(strings.ToLower(stderr), "no merge base")
}

// cappedBuffer accumulates writes up to limit bytes and records whether any
// further bytes were dropped. It always reports the full write length so the
// producing git process never sees a short-write error and blocks.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

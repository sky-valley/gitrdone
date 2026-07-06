package httpapi

import (
	"bytes"
	"context"
	"fmt"
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

	// Resolve every revision to a commit before streaming so an unknown or
	// ambiguous id is a clean 404 instead of a truncated 200.
	for _, rev := range target.revs {
		if !gitRevisionExists(ctx, gitPath, grant.RepoPath, rev) {
			http.Error(w, "diff revision was not found", http.StatusNotFound)
			return nil
		}
	}

	patch, truncated, err := runGitDiff(ctx, gitPath, gitDiffArgs(grant.RepoPath, target))
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if truncated {
		w.Header().Set("X-Git-Diff-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(patch)
	return nil
}

func gitDiffArgs(repoPath string, target gitDiffTarget) []string {
	// --no-color/--no-ext-diff/--full-index keep the output a plain, portable,
	// git-apply-able patch and block external diff/textconv drivers a crafted
	// repo tree could otherwise invoke.
	common := []string{"--no-color", "--no-ext-diff", "--full-index"}
	switch target.kind {
	case gitDiffShow:
		args := []string{"--git-dir", repoPath, "show", "--patch", "--format="}
		args = append(args, common...)
		return append(args, target.revspec, "--")
	default:
		args := []string{"--git-dir", repoPath, "diff"}
		args = append(args, common...)
		return append(args, target.revspec, "--")
	}
}

func gitRevisionExists(ctx context.Context, gitPath string, repoPath string, rev string) bool {
	cmd := exec.CommandContext(ctx, gitPath, "--git-dir", repoPath, "cat-file", "-e", rev+"^{commit}")
	cmd.Env = gitProcessEnv()
	return cmd.Run() == nil
}

func runGitDiff(ctx context.Context, gitPath string, args []string) ([]byte, bool, error) {
	stdout := &cappedBuffer{limit: maxGitDiffBytes}
	stderr := &cappedBuffer{limit: maxGitDiffStderrBytes}

	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Env = gitProcessEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if message := strings.TrimSpace(stderr.buf.String()); message != "" {
			return nil, false, fmt.Errorf("git diff failed: %w: %s", err, message)
		}
		return nil, false, fmt.Errorf("git diff failed: %w", err)
	}
	return stdout.buf.Bytes(), stdout.truncated, nil
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

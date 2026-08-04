package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// Read-only raw file endpoint layered over the git object store. A caller with
// a read-scoped repo token fetches one file's exact bytes at a commit
// (`GET /git/repos/{repoGitID}/raw/{objectID}/{path...}`) without cloning —
// the read path for binary artifact content such as report screenshots. The
// revision is a validated lowercase hex object id and every path segment is
// checked, so neither can be interpreted as a git flag or filesystem path.

const (
	// A raw body is buffered under this cap. Unlike a diff, a truncated file
	// is corrupt, so an over-cap file is refused outright rather than served
	// short.
	maxGitRawBytes = 16 * 1024 * 1024
)

// gitRawTarget is the validated description of what to serve: a pre-verified
// object id and a clean tree path.
type gitRawTarget struct {
	rev  string
	path string
}

type gitRawBackend interface {
	ServeGitRawFile(w http.ResponseWriter, r *http.Request, grant gitAccessGrant, target gitRawTarget) error
}

func gitRawHandler(access gitAccessAuthorizer, backend gitRawBackend) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := parseRepoControlID(r.PathValue("repoID"))
		if !ok {
			http.Error(w, "repo id is invalid", http.StatusBadRequest)
			return
		}
		target, ok := parseGitRawSpec(r.PathValue("spec"))
		if !ok {
			http.Error(w, "raw file spec is invalid", http.StatusBadRequest)
			return
		}

		grant, err := access.AuthorizeGitAccess(r.Context(), authorizeGitAccessInput{
			RepoID:    repoID,
			Token:     repoTokenFromRequest(r),
			Operation: gitOperationRead,
		})
		if err != nil {
			writeGitAccessError(w, err)
			return
		}

		if err := backend.ServeGitRawFile(w, r, grant, target); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	})
}

// parseGitRawSpec splits "<objectID>/<path...>" and validates both halves. The
// path must be a clean tree path: non-empty segments, no "." or ".." — git
// would not resolve those against the filesystem in a bare repo, but a raw
// endpoint should not accept traversal-shaped input at all.
func parseGitRawSpec(raw string) (gitRawTarget, bool) {
	rev, path, ok := strings.Cut(strings.TrimSpace(raw), "/")
	if !ok || !isGitObjectID(rev) {
		return gitRawTarget{}, false
	}
	if path == "" {
		return gitRawTarget{}, false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return gitRawTarget{}, false
		}
	}
	return gitRawTarget{rev: rev, path: path}, true
}

type execGitRawBackend struct {
	gitPath string
}

func (backend execGitRawBackend) ServeGitRawFile(w http.ResponseWriter, r *http.Request, grant gitAccessGrant, target gitRawTarget) error {
	gitPath := strings.TrimSpace(backend.gitPath)
	if gitPath == "" {
		gitPath = "git"
	}
	ctx := r.Context()

	resolvedRev, err := resolveGitCommit(ctx, gitPath, grant.RepoPath, target.rev)
	if err != nil {
		if errors.Is(err, errGitRevisionNotFound) {
			http.Error(w, "raw file revision was not found", http.StatusNotFound)
			return nil
		}
		return err
	}

	// rev:path is a tree lookup inside the object store; the validated hex rev
	// keeps the combined argument from ever parsing as an option.
	spec := resolvedRev + ":" + target.path

	objectType, err := gitObjectType(ctx, gitPath, grant.RepoPath, spec)
	if err != nil {
		http.Error(w, "raw file path was not found at revision", http.StatusNotFound)
		return nil
	}
	if objectType != "blob" {
		http.Error(w, "raw file path is not a file", http.StatusBadRequest)
		return nil
	}

	content := &cappedBuffer{limit: maxGitRawBytes}
	stderr := &cappedBuffer{limit: maxGitDiffStderrBytes}
	cmd := exec.CommandContext(ctx, gitPath, "--git-dir", grant.RepoPath, "cat-file", "blob", spec)
	cmd.Env = gitProcessEnv()
	cmd.Stdout = content
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if content.truncated {
		// Refuse rather than serve corrupt bytes; the cap is a service limit,
		// not a streaming budget.
		http.Error(w, "raw file exceeds the fetch limit", http.StatusRequestEntityTooLarge)
		return nil
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(content.buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content.buf.Bytes())
	return nil
}

// gitObjectType resolves the object type at spec ("<rev>:<path>"); an error
// means the path does not exist at that revision.
func gitObjectType(ctx context.Context, gitPath string, repoPath string, spec string) (string, error) {
	cmd := exec.CommandContext(ctx, gitPath, "--git-dir", repoPath, "cat-file", "-t", spec)
	cmd.Env = gitProcessEnv()
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

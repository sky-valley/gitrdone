package httpapi

import (
	"net/http"
	"strings"
	"time"
)

type createRepoRequest struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
}

type createRepoResponse struct {
	ID            string `json:"id"`
	Repo          string `json:"repo"`
	GitURL        string `json:"gitUrl"`
	DefaultBranch string `json:"defaultBranch"`
}

type getRepoResponse struct {
	ID       string `json:"id"`
	Repo     string `json:"repo"`
	GitURL   string `json:"gitUrl"`
	Archived bool   `json:"archived"`
}

type archiveRepoResponse struct {
	ID         string `json:"id"`
	Repo       string `json:"repo"`
	Archived   bool   `json:"archived"`
	ArchivedAt string `json:"archivedAt"`
}

func createRepoHandler(repos repoCreator, baseURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request createRepoRequest
		if err := decodeJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid JSON for create repo")
			return
		}

		request.Namespace = strings.TrimSpace(request.Namespace)
		request.Name = strings.TrimSpace(request.Name)
		request.DefaultBranch = strings.TrimSpace(request.DefaultBranch)
		if request.DefaultBranch == "" {
			request.DefaultBranch = "main"
		}

		if request.Namespace == "" {
			writeError(w, http.StatusBadRequest, "namespace is required")
			return
		}
		if request.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		repo, err := repos.CreateRepo(r.Context(), createRepoInput{
			Namespace:     request.Namespace,
			Name:          request.Name,
			DefaultBranch: request.DefaultBranch,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "repo could not be created")
			return
		}

		writeJSON(w, http.StatusCreated, createRepoResponse{
			ID:            formatRepoControlID(repo.ID),
			Repo:          repo.Namespace + "/" + repo.Name,
			GitURL:        repoGitURL(baseURL, repo.ID),
			DefaultBranch: repo.DefaultBranch,
		})
	})
}

func getRepoHandler(repos repoGetter, baseURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawRepoID := strings.TrimSpace(r.PathValue("repoID"))
		if rawRepoID == "" {
			writeError(w, http.StatusBadRequest, "repo id is required")
			return
		}
		repoID, ok := parseRepoControlID(rawRepoID)
		if !ok {
			writeError(w, http.StatusBadRequest, "repo id is invalid")
			return
		}

		repo, err := repos.GetRepo(r.Context(), getRepoInput{
			ID: repoID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "repo could not be loaded")
			return
		}

		writeJSON(w, http.StatusOK, getRepoResponse{
			ID:       formatRepoControlID(repo.ID),
			Repo:     repo.Namespace + "/" + repo.Name,
			GitURL:   repoGitURL(baseURL, repo.ID),
			Archived: false,
		})
	})
}

func archiveRepoHandler(repos repoArchiver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawRepoID := strings.TrimSpace(r.PathValue("repoID"))
		if rawRepoID == "" {
			writeError(w, http.StatusBadRequest, "repo id is required")
			return
		}
		repoID, ok := parseRepoControlID(rawRepoID)
		if !ok {
			writeError(w, http.StatusBadRequest, "repo id is invalid")
			return
		}

		repo, err := repos.ArchiveRepo(r.Context(), archiveRepoInput{
			ID: repoID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "repo could not be archived")
			return
		}

		writeJSON(w, http.StatusOK, archiveRepoResponse{
			ID:         formatRepoControlID(repo.ID),
			Repo:       repo.Namespace + "/" + repo.Name,
			Archived:   true,
			ArchivedAt: repo.ArchivedAt.Format(time.RFC3339),
		})
	})
}

package grdclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type reviewResponse struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Concern   string `json:"concern"`
	Reviewer  string `json:"reviewer"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

type reviewObligation struct {
	Version        string          `json:"version"`
	Concern        string          `json:"concern"`
	Reviewer       string          `json:"reviewer"`
	Reason         string          `json:"reason"`
	Evidence       []string        `json:"evidence"`
	LatestResponse *reviewResponse `json:"latestResponse"`
}

type reviewListResponse struct {
	Reviews    []reviewObligation `json:"reviews"`
	NextCursor string             `json:"nextCursor"`
}

func (client Client) Reviews(ctx context.Context, workdir string) error {
	client = client.withDefaults()
	origin, err := discoverRemote(ctx, workdir)
	if err != nil {
		return err
	}
	reviews, err := client.listReviews(ctx, origin)
	if err != nil {
		return err
	}
	if len(reviews) == 0 {
		fmt.Fprintln(client.Stdout, "No reviews assigned.")
		return nil
	}
	for index, review := range reviews {
		if err := rememberReview(ctx, workdir, origin.repoID, review); err != nil {
			return err
		}
		if index > 0 {
			fmt.Fprintln(client.Stdout)
		}
		fmt.Fprintf(client.Stdout, "Review %s — %s\n", reviewHandle(review), review.Concern)
		fmt.Fprintf(client.Stdout, "Why: %s\n", review.Reason)
		for _, evidence := range review.Evidence {
			fmt.Fprintf(client.Stdout, "  - %s\n", evidence)
		}
		if review.LatestResponse != nil && review.LatestResponse.Decision == "changes_requested" {
			fmt.Fprintf(client.Stdout, "Changes requested: %s\n", review.LatestResponse.Rationale)
		}
	}
	return nil
}

func (client Client) RespondReview(ctx context.Context, workdir, handle, decision, rationale string) error {
	client = client.withDefaults()
	handle = strings.TrimSpace(handle)
	rationale = strings.TrimSpace(rationale)
	if handle == "" || rationale == "" {
		return errors.New("review handle and rationale are required")
	}
	if decision != "approved" && decision != "changes_requested" {
		return errors.New("review decision must be approved or changes_requested")
	}
	origin, err := discoverRemote(ctx, workdir)
	if err != nil {
		return err
	}
	reviews, err := client.listReviews(ctx, origin)
	if err != nil {
		return err
	}
	var selected reviewObligation
	found := false
	for _, review := range reviews {
		if err := rememberReview(ctx, workdir, origin.repoID, review); err != nil {
			return err
		}
		if reviewHandle(review) != handle {
			continue
		}
		if found {
			return errors.New("review handle is ambiguous; run grd reviews again")
		}
		selected, found = review, true
	}
	if !found {
		selected, found, err = loadRememberedReview(ctx, workdir, origin.repoID, handle)
		if err != nil {
			return err
		}
	}
	if !found || reviewHandle(selected) != handle {
		return errors.New("review handle is unknown; run grd reviews")
	}
	attempt, err := prepareReviewResponseAttempt(ctx, workdir, origin.repoID, selected, decision, rationale)
	if err != nil {
		return err
	}
	response, err := client.respondReview(ctx, origin, selected, decision, rationale, attempt.IdempotencyKey)
	if err != nil {
		return err
	}
	if response.Version != selected.Version || response.Concern != selected.Concern || response.Decision != decision {
		return errors.New("server returned a different review response")
	}
	if err := forgetReviewResponseAttempt(ctx, workdir, origin.repoID, handle); err != nil {
		return err
	}
	if decision == "approved" {
		fmt.Fprintf(client.Stdout, "Approved %s.\n", handle)
	} else {
		fmt.Fprintf(client.Stdout, "Requested changes for %s.\n", handle)
	}
	return nil
}

func (client Client) listReviews(ctx context.Context, origin remote) ([]reviewObligation, error) {
	var result []reviewObligation
	cursor := ""
	for {
		path := "/v1/repos/" + origin.repoID + "/reviews?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var page reviewListResponse
		if err := client.getJSON(ctx, origin, path, &page); err != nil {
			return nil, fmt.Errorf("list assigned reviews: %w", err)
		}
		for _, review := range page.Reviews {
			if review.Version == "" || review.Concern == "" || review.Reviewer == "" || review.Reason == "" {
				return nil, errors.New("list assigned reviews: server returned an invalid review")
			}
		}
		result = append(result, page.Reviews...)
		if page.NextCursor == "" {
			return result, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("list assigned reviews: server repeated its cursor")
		}
		cursor = page.NextCursor
	}
}

func (client Client) respondReview(ctx context.Context, origin remote, review reviewObligation, decision, rationale, idempotencyKey string) (reviewResponse, error) {
	body, err := json.Marshal(map[string]string{
		"version":   review.Version,
		"concern":   review.Concern,
		"decision":  decision,
		"rationale": rationale,
	})
	if err != nil {
		return reviewResponse{}, fmt.Errorf("encode review response: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin.baseURL+"/v1/repos/"+origin.repoID+"/review-responses", bytes.NewReader(body))
	if err != nil {
		return reviewResponse{}, fmt.Errorf("create review response request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.SetBasicAuth(origin.username, origin.password)
	var response reviewResponse
	if err := client.doJSON(request, &response); err != nil {
		return reviewResponse{}, fmt.Errorf("record review response: %w", err)
	}
	return response, nil
}

func (client Client) withDefaults() Client {
	if client.Stdout == nil {
		client.Stdout = io.Discard
	}
	if client.HTTPClient == nil {
		client.HTTPClient = http.DefaultClient
	}
	return client
}

func reviewHandle(review reviewObligation) string {
	digest := sha256.Sum256([]byte(review.Version + "\x00" + review.Concern))
	return hex.EncodeToString(digest[:5])
}

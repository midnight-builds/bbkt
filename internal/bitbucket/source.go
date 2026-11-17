package bitbucket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// srcPath builds /repositories/{ws}/{repo}/src/{ref}/{path} with a validated
// ref (default HEAD) and, when path is non-empty, a validated file path. With
// an empty path the result ends at the ref (no trailing slash). The path is
// normalized first, so "." or "/" mean the repository root.
func srcPath(workspace, repoSlug, ref, path string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	path, err := cleanRepoPath("path", path)
	if err != nil {
		return "", err
	}
	refSeg, err := pathSegment("ref", ref)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("/repositories/%s/%s/src/%s",
		QueryEscape(workspace), QueryEscape(repoSlug), refSeg)
	if path != "" {
		p, err := pathMulti("path", path)
		if err != nil {
			return "", err
		}
		endpoint += "/" + p
	}
	return endpoint, nil
}

// resolveSlashRef turns a branch or tag name containing '/' (for example
// "feature/x") into its commit hash. Bitbucket's src and filehistory
// endpoints split the ref at the slash, escaped or not, and answer "Commit
// not found"; a hash has no slash. Other refs are returned unchanged.
func (c *Client) resolveSlashRef(workspace, repoSlug, ref string) (string, error) {
	if !strings.Contains(ref, "/") {
		return ref, nil
	}
	name, err := pathSegment("ref", ref)
	if err != nil {
		return "", err
	}
	var target struct {
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	for _, kind := range []string{"branches", "tags"} {
		status, data, err := c.Request(http.MethodGet, fmt.Sprintf("/repositories/%s/%s/refs/%s/%s",
			QueryEscape(workspace), QueryEscape(repoSlug), kind, name), nil)
		if err != nil {
			return "", fmt.Errorf("resolving ref %q: %w", ref, err)
		}
		if status == http.StatusNotFound {
			continue
		}
		if status >= 400 {
			return "", fmt.Errorf("resolving ref %q: %w", ref, parseAPIError(status, data))
		}
		if err := json.Unmarshal(data, &target); err == nil && target.Target.Hash != "" {
			return target.Target.Hash, nil
		}
	}
	return "", fmt.Errorf("no branch or tag named %q", ref)
}

type GetFileContentArgs struct {
	Workspace string `json:"workspace" jsonschema:"Workspace slug"`
	RepoSlug  string `json:"repo_slug" jsonschema:"Repository slug"`
	Path      string `json:"path" jsonschema:"Path to the file"`
	Ref       string `json:"ref,omitempty" jsonschema:"Commit hash, branch, or tag (default: HEAD)"`
}

// GetFileContent reads a file's content from the repository.
func (c *Client) GetFileContent(args GetFileContentArgs) (content []byte, contentType string, err error) {
	if args.Workspace == "" || args.RepoSlug == "" || args.Path == "" {
		return nil, "", fmt.Errorf("workspace, repo_slug, and path are required")
	}

	ref, err := c.resolveSlashRef(args.Workspace, args.RepoSlug, args.Ref)
	if err != nil {
		return nil, "", err
	}
	endpoint, err := srcPath(args.Workspace, args.RepoSlug, ref, args.Path)
	if err != nil {
		return nil, "", err
	}

	return c.GetRaw(endpoint)
}

type ListDirectoryArgs struct {
	Workspace string `json:"workspace" jsonschema:"Workspace slug"`
	RepoSlug  string `json:"repo_slug" jsonschema:"Repository slug"`
	Path      string `json:"path,omitempty" jsonschema:"Path to the directory"`
	Ref       string `json:"ref,omitempty" jsonschema:"Commit hash, branch, or tag (default: HEAD)"`
	Pagelen   int    `json:"pagelen,omitempty" jsonschema:"Results per page (default: 100)"`
	MaxDepth  int    `json:"max_depth,omitempty" jsonschema:"Maximum depth of recursion (default: 1)"`
}

// ListDirectory lists files and directories at a given path.
func (c *Client) ListDirectory(args ListDirectoryArgs) (*Paginated[TreeEntry], error) {
	if args.Workspace == "" || args.RepoSlug == "" {
		return nil, fmt.Errorf("workspace and repo_slug are required")
	}

	pagelen := args.Pagelen
	if pagelen == 0 {
		pagelen = 100
	}
	maxDepth := args.MaxDepth
	if maxDepth == 0 {
		maxDepth = 1
	}

	cleaned, err := cleanRepoPath("path", args.Path)
	if err != nil {
		return nil, err
	}
	ref, err := c.resolveSlashRef(args.Workspace, args.RepoSlug, args.Ref)
	if err != nil {
		return nil, err
	}
	endpoint, err := srcPath(args.Workspace, args.RepoSlug, ref, cleaned)
	if err != nil {
		return nil, err
	}
	if cleaned == "" {
		endpoint += "/"
	}

	endpoint += fmt.Sprintf("?pagelen=%d&max_depth=%d", pagelen, maxDepth)

	return GetPaginated[TreeEntry](c, endpoint)
}

type GetFileHistoryArgs struct {
	Workspace string `json:"workspace" jsonschema:"Workspace slug"`
	RepoSlug  string `json:"repo_slug" jsonschema:"Repository slug"`
	Path      string `json:"path" jsonschema:"Path to the file"`
	Ref       string `json:"ref,omitempty" jsonschema:"Commit hash, branch, or tag (default: HEAD)"`
	Pagelen   int    `json:"pagelen,omitempty" jsonschema:"Results per page (default: 25)"`
}

// GetFileHistory gets the commit history for a specific file.
func (c *Client) GetFileHistory(args GetFileHistoryArgs) (*Paginated[json.RawMessage], error) {
	if args.Workspace == "" || args.RepoSlug == "" || args.Path == "" {
		return nil, fmt.Errorf("workspace, repo_slug, and path are required")
	}

	ref := args.Ref
	if ref == "" {
		ref = "HEAD"
	}
	pagelen := args.Pagelen
	if pagelen == 0 {
		pagelen = 25
	}

	ref, err := c.resolveSlashRef(args.Workspace, args.RepoSlug, ref)
	if err != nil {
		return nil, err
	}
	refSeg, err := pathSegment("ref", ref)
	if err != nil {
		return nil, err
	}
	cleaned, err := cleanRepoPath("path", args.Path)
	if err != nil {
		return nil, err
	}
	if cleaned == "" {
		return nil, fmt.Errorf("path must name a file, not the repository root")
	}
	filePath, err := pathMulti("path", cleaned)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("/repositories/%s/%s/filehistory/%s/%s?pagelen=%d",
		QueryEscape(args.Workspace), QueryEscape(args.RepoSlug), refSeg, filePath, pagelen)

	// Filehistory returns commit objects with file metadata
	return GetPaginated[json.RawMessage](c, endpoint)
}

type SearchCodeArgs struct {
	Workspace   string `json:"workspace" jsonschema:"Workspace slug"`
	RepoSlug    string `json:"repo_slug" jsonschema:"Repository slug"`
	SearchQuery string `json:"query" jsonschema:"Search query"`
	Pagelen     int    `json:"pagelen,omitempty" jsonschema:"Results per page (default: 25)"`
	Page        int    `json:"page,omitempty" jsonschema:"Page number"`
}

// SearchCode searches for code in a repository using Bitbucket's code search.
func (c *Client) SearchCode(args SearchCodeArgs) ([]byte, error) {
	if args.Workspace == "" || args.RepoSlug == "" || args.SearchQuery == "" {
		return nil, fmt.Errorf("workspace, repo_slug, and query are required")
	}

	pagelen := args.Pagelen
	if pagelen == 0 {
		pagelen = 25
	}
	page := args.Page
	if page == 0 {
		page = 1
	}

	endpoint := fmt.Sprintf("/repositories/%s/%s/search/code?search_query=%s&pagelen=%d&page=%d",
		QueryEscape(args.Workspace), QueryEscape(args.RepoSlug), QueryValueEscape(args.SearchQuery), pagelen, page)

	return c.Get(endpoint)
}

type WriteFileArgs struct {
	Workspace string `json:"workspace" jsonschema:"Workspace slug"`
	RepoSlug  string `json:"repo_slug" jsonschema:"Repository slug"`
	Path      string `json:"path" jsonschema:"Path to the file"`
	Content   string `json:"content" jsonschema:"Content to write to the file"`
	Message   string `json:"message" jsonschema:"Commit message"`
	Branch    string `json:"branch,omitempty" jsonschema:"Branch to commit to"`
	Author    string `json:"author,omitempty" jsonschema:"Commit author in 'Name <email>' format"`
}

// WriteFile writes or updates a file in the repository.
func (c *Client) WriteFile(args WriteFileArgs) error {
	if args.Workspace == "" || args.RepoSlug == "" || args.Path == "" {
		return fmt.Errorf("workspace, repo_slug, and path are required")
	}

	endpoint := fmt.Sprintf("/repositories/%s/%s/src",
		QueryEscape(args.Workspace), QueryEscape(args.RepoSlug))

	fields := make(map[string]string)
	if args.Message != "" {
		fields["message"] = args.Message
	}
	if args.Branch != "" {
		fields["branch"] = args.Branch
	}
	if args.Author != "" {
		fields["author"] = args.Author
	}

	files := map[string][]byte{
		args.Path: []byte(args.Content),
	}

	_, err := c.PostMultipart(endpoint, fields, files)
	if err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}

type DeleteFileArgs struct {
	Workspace string `json:"workspace" jsonschema:"Workspace slug"`
	RepoSlug  string `json:"repo_slug" jsonschema:"Repository slug"`
	Path      string `json:"path" jsonschema:"Path to the file to delete"`
	Message   string `json:"message" jsonschema:"Commit message"`
	Branch    string `json:"branch,omitempty" jsonschema:"Branch to commit to"`
	Author    string `json:"author,omitempty" jsonschema:"Commit author in 'Name <email>' format"`
}

// DeleteFile deletes a file from the repository.
func (c *Client) DeleteFile(args DeleteFileArgs) error {
	if args.Workspace == "" || args.RepoSlug == "" || args.Path == "" {
		return fmt.Errorf("workspace, repo_slug, and path are required")
	}

	endpoint := fmt.Sprintf("/repositories/%s/%s/src",
		QueryEscape(args.Workspace), QueryEscape(args.RepoSlug))

	fields := make(map[string]string)
	if args.Message != "" {
		fields["message"] = args.Message
	}
	if args.Branch != "" {
		fields["branch"] = args.Branch
	}
	if args.Author != "" {
		fields["author"] = args.Author
	}

	// Bitbucket API expects "files" as the key and the path as the value
	// However, we just send it as a regular text field
	fields["files"] = args.Path

	_, err := c.PostMultipart(endpoint, fields, nil)
	if err != nil {
		return fmt.Errorf("deleting file: %w", err)
	}

	return nil
}

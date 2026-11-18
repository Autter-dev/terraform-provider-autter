package provider

import (
	"fmt"
	"strings"
)

// parseImportID splits "<repository>:<name>" at the first colon. The
// repository is its id or owner/repo; the name may contain colons.
func parseImportID(id string) (repository, name string, err error) {
	repository, name, found := strings.Cut(id, ":")
	repository = strings.TrimSpace(repository)
	if !found || repository == "" || strings.TrimSpace(name) == "" {
		return "", "", fmt.Errorf(`expected "<repository>:<name>", where <repository> is the repository's id or owner/repo, for example "acme/web:Checkout errors"; got %q`, id)
	}
	return repository, name, nil
}

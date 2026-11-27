// Command testgit is a manual check of internal/git against a real repo,
// authenticated as the GitHub App (installation token over HTTPS).
//
//	go run ./cmd/testgit
//
// The App ID, installation ID and key path default to the values below and
// can be overridden with the same variables the worker uses:
//
//	DEPLOYCTL_GITHUB_APP_ID
//	DEPLOYCTL_GITHUB_APP_INSTALLATION_ID
//	DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH
//
// Read-only: it only calls GetValue, so it never pushes anything.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/TheTangentLine/deployctl/internal/git"
	"github.com/TheTangentLine/deployctl/internal/githubapp"
	"github.com/TheTangentLine/deployctl/internal/steps"
)

const (
	defaultAppID          = "5888186"
	defaultInstallationID = "185015481"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	appID := env("DEPLOYCTL_GITHUB_APP_ID", defaultAppID)
	instID, err := strconv.ParseInt(env("DEPLOYCTL_GITHUB_APP_INSTALLATION_ID", defaultInstallationID), 10, 64)
	if err != nil {
		log.Fatal("DEPLOYCTL_GITHUB_APP_INSTALLATION_ID is not a number")
	}
	keyPath := env("DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH", filepath.Join(home, ".config", "deployctl", "github-app.pem"))

	src, err := githubapp.NewFromFile(appID, instID, keyPath)
	if err != nil {
		log.Fatal(err)
	}
	repo, err := git.New(filepath.Join(os.TempDir(), "deployctl-testgit"), git.TokenAuth(src.Token))
	if err != nil {
		log.Fatal(err)
	}

	for _, key := range []string{"backend.image.tag", "backend.replicaCount", "frontend.image.tag"} {
		ref := steps.ValueRef{
			Repo:   "https://github.com/TheTangentLine/reconcile.git",
			Branch: "main",
			File:   "charts/reconcile/values.yaml",
			Key:    key,
		}
		v, err := repo.GetValue(ctx, ref)
		if err != nil {
			log.Fatalf("%s: %v (retryable: %v)", key, err, steps.IsRetryable(err))
		}
		fmt.Printf("%-22s %s\n", key, v)
	}
}

// env returns the environment variable name, or def if it is unset or empty.
func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

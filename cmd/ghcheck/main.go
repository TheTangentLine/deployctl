// Command ghcheck checks a GitHub App setup end to end, read-only:
//
//  1. the private key and App ID are accepted (signs a JWT, asks GitHub)
//  2. the installation ID is one of the App's installations
//  3. an installation token can be obtained
//  4. that token can read the config repo through internal/git
//
// It never pushes and never prints the token or the key.
//
//	DEPLOYCTL_GITHUB_APP_ID=5888186 \
//	DEPLOYCTL_GITHUB_APP_INSTALLATION_ID=185015481 \
//	DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH=$HOME/.config/deployctl/github-app.pem \
//	go run ./cmd/ghcheck
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/TheTangentLine/deployctl/internal/git"
	"github.com/TheTangentLine/deployctl/internal/githubapp"
	"github.com/TheTangentLine/deployctl/internal/steps"
)

const (
	repoURL = "https://github.com/TheTangentLine/reconcile.git"
	file    = "charts/reconcile/values.yaml"
	key     = "backend.image.tag"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	appID := os.Getenv("DEPLOYCTL_GITHUB_APP_ID")
	instID, err := strconv.ParseInt(os.Getenv("DEPLOYCTL_GITHUB_APP_INSTALLATION_ID"), 10, 64)
	if err != nil {
		fail("DEPLOYCTL_GITHUB_APP_INSTALLATION_ID is not a number")
	}
	keyPath := os.Getenv("DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH")
	if appID == "" || keyPath == "" {
		fail("set DEPLOYCTL_GITHUB_APP_ID and DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH")
	}
	if info, err := os.Stat(keyPath); err == nil && info.Mode().Perm()&0o077 != 0 {
		fmt.Printf("note: %s is readable by other users; run: chmod 600 %s\n", keyPath, keyPath)
	}

	src, err := githubapp.NewFromFile(appID, instID, keyPath)
	if err != nil {
		fail(err.Error())
	}

	// 1 and 2: authenticate as the App and list its installations.
	insts, err := src.Installations(ctx)
	if err != nil {
		fail("App authentication failed: " + err.Error())
	}
	ok("App ID and private key accepted")
	found := false
	for _, in := range insts {
		mark := " "
		if in.ID == instID {
			mark, found = "*", true
		}
		fmt.Printf("     %s installation %d on %s (repositories: %s)\n", mark, in.ID, in.Account, in.RepositorySelection)
	}
	if !found {
		fail(fmt.Sprintf("installation %d is not one of this App's installations; use one of the IDs above", instID))
	}
	ok(fmt.Sprintf("installation %d belongs to this App", instID))

	// 3: get an installation token.
	if _, err := src.Token(ctx); err != nil {
		fail("could not get an installation token: " + err.Error())
	}
	ok("installation token obtained (valid for one hour; not shown)")

	// 4: read the config repo with it, through the real Git code.
	repo, err := git.New(filepath.Join(os.TempDir(), "deployctl-ghcheck"), git.TokenAuth(src.Token))
	if err != nil {
		fail(err.Error())
	}
	v, err := repo.GetValue(ctx, steps.ValueRef{Repo: repoURL, Branch: "main", File: file, Key: key})
	if err != nil {
		fail("reading the repo with the token failed: " + err.Error())
	}
	ok(fmt.Sprintf("read %s from %s: %s", key, file, v))
	fmt.Println("\nAll checks passed. Nothing was pushed.")
}

func ok(msg string) { fmt.Println("  ✔ " + msg) }

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "  ✖ "+msg)
	os.Exit(1)
}

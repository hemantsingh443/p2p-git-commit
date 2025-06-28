package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// CommitAndPush performs `git checkout`, `git add`, `git commit`, and `git push`.
func CommitAndPush(repoPath, commitMessage, remote, branch string) (string, error) {
	// Step 1: Git Checkout. Switch to the correct branch first.
	cmdCheckout := exec.Command("git", "checkout", branch)
	cmdCheckout.Dir = repoPath
	out, err := cmdCheckout.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git checkout failed: %w", err)
	}

	// Step 2: Git Add
	cmdAdd := exec.Command("git", "add", ".")
	cmdAdd.Dir = repoPath
	out, err = cmdAdd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git add failed: %w", err)
	}

	// Step 3: Git Commit
	cmdCommit := exec.Command("git", "commit", "-m", commitMessage)
	cmdCommit.Dir = repoPath
	out, err = cmdCommit.CombinedOutput()
	if err != nil {
		// If there's nothing to commit, it's not a fatal error for our use case.
		// We can check the output for "nothing to commit".
		if strings.Contains(string(out), "nothing to commit") {
			return "Working tree is clean. Nothing to commit.", nil
		}
		return string(out), fmt.Errorf("git commit failed: %w", err)
	}

	// Step 4: Git Push
	cmdPush := exec.Command("git", "push", remote, branch)
	cmdPush.Dir = repoPath
	out, err = cmdPush.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git push failed: %w", err)
	}

	return fmt.Sprintf("Successfully pushed to %s/%s\n%s", remote, branch, string(out)), nil
}

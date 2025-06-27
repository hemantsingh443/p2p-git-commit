package git

import (
	"fmt"
	"os/exec"
)

// CommitAndPush performs `git add`, `git commit`, and `git push`.
func CommitAndPush(repoPath, commitMessage, remote, branch string) (string, error) {
	// Git Add
	cmdAdd := exec.Command("git", "add", ".")
	cmdAdd.Dir = repoPath
	out, err := cmdAdd.CombinedOutput()
	if err != nil {
		if len(out) == 0 {
			return err.Error(), fmt.Errorf("git add failed: %w", err)
		}
		return string(out), fmt.Errorf("git add failed: %w", err)
	}

	// Git Commit
	cmdCommit := exec.Command("git", "commit", "-m", commitMessage)
	cmdCommit.Dir = repoPath
	out, err = cmdCommit.CombinedOutput()
	if err != nil {
		if len(out) == 0 {
			return err.Error(), fmt.Errorf("git commit failed: %w", err)
		}
		return string(out), fmt.Errorf("git commit failed: %w", err)
	}

	// Git Push
	// CHANGE 'main' to 'master' if that is your branch name
	cmdPush := exec.Command("git", "push", remote, "master") // or "main"
	cmdPush.Dir = repoPath
	out, err = cmdPush.CombinedOutput()
	if err != nil {
		if len(out) == 0 {
			return err.Error(), fmt.Errorf("git push failed: %w", err)
		}
		return string(out), fmt.Errorf("git push failed: %w", err)
	}

	return fmt.Sprintf("Successfully pushed to %s/%s\n%s", remote, branch, string(out)), nil
}

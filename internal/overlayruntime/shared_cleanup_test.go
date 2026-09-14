package overlayruntime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedBridgeRemovalChecksPublicOwnership(t *testing.T) {
	for _, change := range []string{"none", "legacy mode", "content", "mode", "ownership", "tracked"} {
		t.Run(change, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, localRule), "private\n")
			write(t, filepath.Join(repo, ".git", "info", "exclude"), "/AGENTS.local.md\n")
			r, err := resolveContext(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.Root, sharedBridge)
			write(t, path, "@AGENTS.md\n")
			regressionChmod(t, path, 0o644)
			state := RepositoryState{SharedSource: "checkout"}
			if err := recordGeneratedFile(&state, path, []byte("@AGENTS.md\n")); err != nil {
				t.Fatal(err)
			}
			if change == "legacy mode" {
				delete(state.GeneratedModes, path)
			}
			plans, err := r.planManagedFiles("claude", state)
			if err != nil || len(plans) != 1 || !plans[0].Remove || plans[0].Path != path {
				t.Fatalf("shared removal plan: %+v, %v", plans, err)
			}
			issues := r.checkManagedFiles("claude", state)
			if len(issues) != 1 || issues[0] != path+" is stale" {
				t.Fatalf("public bridge requires only cleanup: %v", issues)
			}
			if _, err := r.inspectManagedFile(managedInstructionFile{Path: path}, state, false); err == nil {
				t.Fatal("public bridge accepted private managed write")
			}
			switch change {
			case "content":
				write(t, path, "user edits\n")
			case "mode":
				regressionChmod(t, path, 0o600)
			case "ownership":
				delete(state.Generated, path)
			case "tracked":
				git(t, repo, "add", sharedBridge)
			}
			before := snapshotInstructionFiles(t, repo, repo)
			var changes []string
			err = r.applyManagedFiles(plans, &state, &changes)
			if change == "none" || change == "legacy mode" {
				if err != nil || len(changes) != 1 || exists(path) || state.Generated[path] != "" {
					t.Fatalf("public bridge removal: changes=%v err=%v state=%+v", changes, err, state)
				}
				if _, ok := state.GeneratedModes[path]; ok {
					t.Fatal("removed bridge mode retained")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "preserved") || len(changes) != 0 {
					t.Fatalf("mutation after planning accepted: changes=%v err=%v", changes, err)
				}
				assertInstructionSnapshot(t, before)
			}
		})
	}
}

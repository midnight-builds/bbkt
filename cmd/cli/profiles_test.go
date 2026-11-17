package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zach-snell/bbkt/internal/bitbucket"
)

func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func saveTestProfile(t *testing.T, name string, workspaces ...string) {
	t.Helper()
	err := bitbucket.SaveProfile(&bitbucket.Credentials{
		ProfileName:          name,
		AuthType:             bitbucket.AuthTypeAPIToken,
		Email:                name + "@example.com",
		APIToken:             "tok-" + name,
		AccessibleWorkspaces: workspaces,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetActiveProfile(t *testing.T) {
	isolateHome(t)
	saveTestProfile(t, "a")
	saveTestProfile(t, "b")

	if err := setActiveProfile("b"); err != nil {
		t.Fatal(err)
	}
	store, err := bitbucket.LoadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.ActiveProfile != "b" || len(store.Profiles) != 2 {
		t.Errorf("store = active %q, %d profiles; want b, 2", store.ActiveProfile, len(store.Profiles))
	}

	if err := setActiveProfile("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("setActiveProfile(missing) err = %v, want not found", err)
	}
}

// A refresh merges only workspace lists into the current on-disk store, so a
// profile added (or tokens rotated) after the refresh loaded its snapshot are
// kept.
func TestMergeAccessibleWorkspaces_KeepsConcurrentChanges(t *testing.T) {
	isolateHome(t)
	saveTestProfile(t, "a", "old-ws")

	// Simulate changes made by another process after refresh loaded the store.
	saveTestProfile(t, "added-later")
	if err := bitbucket.UpdateProfileStore(func(s *bitbucket.ProfileStore) error {
		s.Profiles["a"].APIToken = "rotated"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := mergeAccessibleWorkspaces(map[string][]string{
		"a":       {"ws1", "ws2"},
		"deleted": {"ws3"},
	})
	if err != nil {
		t.Fatal(err)
	}

	store, err := bitbucket.LoadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Profiles["added-later"]; !ok {
		t.Error("profile added concurrently was dropped")
	}
	if _, ok := store.Profiles["deleted"]; ok {
		t.Error("profile missing from disk was resurrected")
	}
	a := store.Profiles["a"]
	if a.APIToken != "rotated" {
		t.Errorf("APIToken = %q, want concurrent update kept", a.APIToken)
	}
	if !reflect.DeepEqual(a.AccessibleWorkspaces, []string{"ws1", "ws2"}) {
		t.Errorf("AccessibleWorkspaces = %v, want [ws1 ws2]", a.AccessibleWorkspaces)
	}
}

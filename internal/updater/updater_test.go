package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
)

func fakeGetter(responses map[string][]byte, hits *[]string) Getter {
	return func(_ context.Context, url string) ([]byte, error) {
		*hits = append(*hits, url)
		body, ok := responses[url]
		if !ok {
			return nil, fmt.Errorf("404 %s", url)
		}
		return body, nil
	}
}

func TestLatestReleaseAndAssets(t *testing.T) {
	release := Release{
		TagName: "v0.7.0",
		Assets: []Asset{
			{Name: "kitodock_0.7.0_windows_amd64.zip", BrowserDownloadURL: "u/zip", Size: 10},
			{Name: "kitodock_0.7.0_windows_amd64.msi", BrowserDownloadURL: "u/msi", Size: 20},
			{Name: "checksums.txt"},
		},
	}
	body, _ := json.Marshal(release)
	var hits []string
	checker := &Checker{Repo: "a/b", Current: "0.6.0", Get: fakeGetter(map[string][]byte{
		"https://api.github.com/repos/a/b/releases/latest": body,
	}, &hits)}

	latest, err := checker.Latest(context.Background())
	if err != nil || latest.TagName != "v0.7.0" {
		t.Fatalf("latest = %+v err=%v", latest, err)
	}
	if !IsNewer("0.6.0", "v0.7.0") || IsNewer("0.7.0", "v0.7.0") || IsNewer("0.8.0", "v0.7.1") {
		t.Fatal("IsNewer comparison broken")
	}
	if msi := latest.FindAsset("_windows_amd64.msi"); msi == nil || msi.BrowserDownloadURL != "u/msi" {
		t.Fatalf("msi asset = %+v", msi)
	}
	if latest.FindAsset("_linux_amd64.tar.gz") != nil {
		t.Fatal("nonexistent asset found")
	}
}

func TestDownloadAndVerify(t *testing.T) {
	artifact := []byte("kitodock-installer-bytes")
	sum := sha256.Sum256(artifact)
	digest := hex.EncodeToString(sum[:])
	wrong := sha256.Sum256([]byte("tampered"))
	wrongHex := hex.EncodeToString(wrong[:])

	var hits []string
	get := fakeGetter(map[string][]byte{
		"https://x/artifact.msi":        artifact,
		"https://x/artifact.msi.sha256": []byte(digest + "  artifact.msi\n"),
		"https://x/bad.sha256":          []byte(wrongHex),
	}, &hits)

	got, err := DownloadAndVerify(context.Background(), get, "https://x/artifact.msi", "https://x/artifact.msi.sha256", int64(len(artifact)))
	if err != nil || string(got) != string(artifact) {
		t.Fatalf("verified download = %v err=%v", got, err)
	}

	// Tampered digest fails loudly.
	if _, err := DownloadAndVerify(context.Background(), get, "https://x/artifact.msi", "https://x/bad.sha256", int64(len(artifact))); err == nil {
		t.Fatal("mismatched digest accepted")
	}
	// Size mismatch fails.
	if _, err := DownloadAndVerify(context.Background(), get, "https://x/artifact.msi", "https://x/artifact.msi.sha256", 999); err == nil {
		t.Fatal("wrong size accepted")
	}
}

func TestLatestRejectsEmpty(t *testing.T) {
	var hits []string
	checker := &Checker{Repo: "a/b", Get: fakeGetter(map[string][]byte{
		"https://api.github.com/repos/a/b/releases/latest": []byte(`{}`),
	}, &hits)}
	if _, err := checker.Latest(context.Background()); err == nil {
		t.Fatal("empty release accepted")
	}
}

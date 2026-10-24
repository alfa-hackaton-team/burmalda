package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadManifestRejectsChangedFiles(t *testing.T) {
	dir := t.TempDir()
	manifest := Manifest{Version: "test-v1"}
	for name := range requiredCSV {
		data := []byte(name + "\n")
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, File{Name: name, Kind: "csv", SHA256: hash(data), SizeBytes: int64(len(data))})
	}
	for i := range 9 {
		name := "report-" + string(rune('a'+i)) + ".pdf"
		data := []byte("%PDF-test " + name)
		text := []byte("Provided text " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(textPath(filepath.Join(dir, name)), text, 0600); err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, File{Name: name, Kind: "pdf", SHA256: hash(data), SizeBytes: int64(len(data)), TextSHA256: hash(text)})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadManifest(dir); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report-a.txt"), []byte("changed provided text"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), "provided text mismatch") {
		t.Fatalf("expected TXT integrity error, got %v", err)
	}
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestProvidedCorpusTXTMatchesManifest(t *testing.T) {
	dir := "../../../data/hackathon"
	manifest, _, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest.Files {
		if file.Kind != "pdf" {
			continue
		}
		body, err := os.ReadFile(textPath(filepath.Join(dir, file.Name)))
		if err != nil || !utf8.Valid(body) {
			t.Fatalf("invalid TXT for %s: %v", file.Name, err)
		}
		if _, err := reportPages(string(body)); err != nil {
			t.Fatalf("page markers for %s: %v", file.Name, err)
		}
	}
}

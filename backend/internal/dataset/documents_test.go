package dataset

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportPagesUsesUTF8ByteOffsets(t *testing.T) {
	body := "\n\n=== Page 1 ===\nПривет\n\n=== Page 2 ===\nМир"
	pages, err := reportPages(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Start != 2 || pages[0].End != strings.Index(body, "=== Page 2 ===") ||
		pages[1].End != len(body) || !strings.Contains(body[pages[0].Start:pages[0].End], "Привет") {
		t.Fatalf("wrong page offsets: %+v", pages)
	}
	if _, err := reportPages("=== Page 1 ===\nA\n=== Page 3 ===\nB"); err == nil {
		t.Fatal("nonconsecutive page numbers accepted")
	}
}

func TestDatasetRoutesRequireToken(t *testing.T) {
	mux := http.NewServeMux()
	DatasetHandler{Token: "secret"}.Register(mux)
	for _, authorization := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, datasetPath, nil)
		req.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%q: got %d", authorization, response.Code)
		}
	}
	mux = http.NewServeMux()
	DatasetHandler{}.Register(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, datasetPath, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled dataset route: got %d", response.Code)
	}
}

func TestSnapshotIDTracksNormalizationPolicy(t *testing.T) {
	if snapshotID("bundle-a", 1) == snapshotID("bundle-a", 2) ||
		snapshotID("bundle-a", 1) == snapshotID("bundle-b", 1) ||
		snapshotID("bundle-a", 1) != snapshotID("bundle-a", 1) {
		t.Fatal("snapshot identity is not stable and versioned")
	}
}

func TestProvidedTextPageHeaders(t *testing.T) {
	for _, body := range []string{"=== Страница 7.1 ===\nТекст\n=== Страница 7.2 ===\nКонец", "[Скриншот 3.1]\nТекст\n[Скриншот 3.2]\nКонец"} {
		pages, err := reportPages(body)
		if err != nil || len(pages) != 2 || pages[0].Number != 1 || pages[1].End != len(body) {
			t.Fatalf("provided markers %+v %v", pages, err)
		}
	}
}

package markets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockBook returns an httptest.Server that serves a fixed order book JSON.
func mockBook(bids, asks []bookLevel) *httptest.Server {
	book := orderBook{Bids: bids, Asks: asks}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(book)
	}))
}

func TestDepthWeightedPrice(t *testing.T) {
	t.Run("empty levels returns 0", func(t *testing.T) {
		if got := DepthWeightedPrice(nil, 5); got != 0 {
			t.Fatalf("want 0, got %v", got)
		}
	})

	t.Run("single level equals price", func(t *testing.T) {
		levels := []bookLevel{{Price: "0.60", Size: "100"}}
		if got := DepthWeightedPrice(levels, 5); got != 0.60 {
			t.Fatalf("want 0.60, got %v", got)
		}
	})

	t.Run("topN clips depth", func(t *testing.T) {
		// Only first 2 levels used; third level (0.40) should be ignored.
		levels := []bookLevel{
			{Price: "0.60", Size: "100"},
			{Price: "0.58", Size: "100"},
			{Price: "0.40", Size: "10000"},
		}
		got := DepthWeightedPrice(levels, 2)
		want := (0.60*100 + 0.58*100) / 200.0
		if abs(got-want) > 1e-9 {
			t.Fatalf("want %.6f, got %.6f", want, got)
		}
	})

	t.Run("weighted average is size-weighted", func(t *testing.T) {
		levels := []bookLevel{
			{Price: "0.70", Size: "200"},
			{Price: "0.60", Size: "100"},
		}
		got := DepthWeightedPrice(levels, 5)
		want := (0.70*200 + 0.60*100) / 300.0
		if abs(got-want) > 1e-9 {
			t.Fatalf("want %.6f, got %.6f", want, got)
		}
	})

	t.Run("invalid parse skips level", func(t *testing.T) {
		levels := []bookLevel{
			{Price: "bad", Size: "100"},
			{Price: "0.50", Size: "200"},
		}
		got := DepthWeightedPrice(levels, 5)
		if abs(got-0.50) > 1e-9 {
			t.Fatalf("want 0.50, got %v", got)
		}
	})

	t.Run("zero size skips level", func(t *testing.T) {
		levels := []bookLevel{
			{Price: "0.80", Size: "0"},
			{Price: "0.50", Size: "100"},
		}
		got := DepthWeightedPrice(levels, 5)
		if abs(got-0.50) > 1e-9 {
			t.Fatalf("want 0.50, got %v", got)
		}
	})
}

func TestFetchFairValue(t *testing.T) {
	t.Run("mid-point between VWAP bid and ask", func(t *testing.T) {
		bids := []bookLevel{{Price: "0.58", Size: "100"}}
		asks := []bookLevel{{Price: "0.62", Size: "100"}}
		srv := mockBook(bids, asks)
		defer srv.Close()

		origURL := clobBookURL
		clobBookURL = srv.URL
		defer func() { clobBookURL = origURL }()
		fairValueHTTPClient = srv.Client()

		fairYes, fairNo, err := FetchFairValue("tok1")
		if err != nil {
			t.Fatal(err)
		}
		wantYes := (0.58 + 0.62) / 2
		if abs(fairYes-wantYes) > 1e-9 {
			t.Fatalf("fairYes: want %.4f, got %.4f", wantYes, fairYes)
		}
		if abs(fairNo-(1-wantYes)) > 1e-9 {
			t.Fatalf("fairNo: want %.4f, got %.4f", 1-wantYes, fairNo)
		}
	})

	t.Run("only asks available", func(t *testing.T) {
		asks := []bookLevel{{Price: "0.62", Size: "150"}}
		srv := mockBook(nil, asks)
		defer srv.Close()

		clobBookURL = srv.URL
		fairValueHTTPClient = srv.Client()

		fairYes, _, err := FetchFairValue("tok2")
		if err != nil {
			t.Fatal(err)
		}
		if abs(fairYes-0.62) > 1e-9 {
			t.Fatalf("want 0.62, got %.4f", fairYes)
		}
	})

	t.Run("only bids available", func(t *testing.T) {
		bids := []bookLevel{{Price: "0.55", Size: "200"}}
		srv := mockBook(bids, nil)
		defer srv.Close()

		clobBookURL = srv.URL
		fairValueHTTPClient = srv.Client()

		fairYes, _, err := FetchFairValue("tok3")
		if err != nil {
			t.Fatal(err)
		}
		if abs(fairYes-0.55) > 1e-9 {
			t.Fatalf("want 0.55, got %.4f", fairYes)
		}
	})

	t.Run("empty book returns error", func(t *testing.T) {
		srv := mockBook(nil, nil)
		defer srv.Close()

		clobBookURL = srv.URL
		fairValueHTTPClient = srv.Client()

		_, _, err := FetchFairValue("tok4")
		if err == nil {
			t.Fatal("expected error for empty book")
		}
	})

	t.Run("fairNo = 1 - fairYes", func(t *testing.T) {
		bids := []bookLevel{{Price: "0.45", Size: "100"}}
		asks := []bookLevel{{Price: "0.55", Size: "100"}}
		srv := mockBook(bids, asks)
		defer srv.Close()

		clobBookURL = srv.URL
		fairValueHTTPClient = srv.Client()

		fairYes, fairNo, err := FetchFairValue("tok5")
		if err != nil {
			t.Fatal(err)
		}
		if abs(fairYes+fairNo-1.0) > 1e-9 {
			t.Fatalf("fairYes+fairNo should equal 1, got %.6f", fairYes+fairNo)
		}
	})
}


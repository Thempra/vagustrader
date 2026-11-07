package clob

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestCreds builds throwaway L2 credentials. The secret must be valid
// base64 or BuildHMACSignature returns "" and every authed call fails.
func newTestCreds() *ClobCreds {
	return &ClobCreds{
		APIKey:        "test-api-key",
		APISecret:     "dGVzdC1zZWNyZXQtZm9yLXVuaXQtdGVzdHMtb25seQ==",
		APIPassphrase: "test-passphrase",
	}
}

func newTestClient(t *testing.T, srv *httptest.Server) *ClobClient {
	t.Helper()
	return &ClobClient{
		host:   srv.URL,
		signer: newTestSigner(t, 137),
		creds:  newTestCreds(),
		client: &http.Client{},
	}
}

// TestGetOrderUsesDataPrefix pins the endpoint: /order/{id} does not exist on
// the CLOB (404), so fill verification used to fail for every live order and
// the broker refused to record its own fills.
func TestGetOrderUsesDataPrefix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path != "/data/order/0xabc123" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"0xabc123","status":"MATCHED","size_matched":"12.5","price":"0.42"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	st, err := c.GetOrder("0xabc123")
	if err != nil {
		t.Fatalf("GetOrder: %v (path requested: %s)", err, gotPath)
	}
	if st.Status != "MATCHED" || st.SizeFilled != 12.5 || st.Price != 0.42 {
		t.Fatalf("unexpected status: %+v", st)
	}
}

// TestCancelOrderPostsIDInBody pins the cancel contract: DELETE /order with a
// JSON body. DELETE /order/{id} is 404 and leaves orders resting unfilled.
func TestCancelOrderPostsIDInBody(t *testing.T) {
	var method, path string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"canceled":["0xdeadbeef"],"not_canceled":{}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.CancelOrder("0xdeadbeef"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if method != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", method)
	}
	if path != "/order" {
		t.Fatalf("path = %s, want /order (id goes in the body)", path)
	}
	if body["orderID"] != "0xdeadbeef" {
		t.Fatalf("body = %v, want orderID=0xdeadbeef", body)
	}
}

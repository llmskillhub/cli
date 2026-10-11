package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The server's 202 names the run run_id. Reading id instead left the CLI
// printing "queued run " and polling /v1/eval-runs/ -- --wait never worked.
func TestRunEvalReadsTheRunID(t *testing.T) {
	var gotClient, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/eval-runs":
			gotClient = r.Header.Get("X-LLMSH-Client")
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			gotBody = in["eval"] + " " + in["skill"]
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"run_id":"0b6e8c2a-run","status":"queued"}`))
		case "/v1/eval-runs/0b6e8c2a-run":
			w.Write([]byte(`{"id":"0b6e8c2a-run","eval":"bob/sql-safety","status":"succeeded","visibility":"public",
				"score":1,"max_score":2,"counted":false,
				"integrity":[{"flag":"answers_leaked","serious":true,"detail":"6 of 6"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, srv.URL, "")

	run, err := c.RunEval("bob/sql-safety", "alice/sql-tuner")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "0b6e8c2a-run" || run.Status != "queued" {
		t.Fatalf("run = %+v", run)
	}
	if gotClient != "cli" || gotBody != "bob/sql-safety alice/sql-tuner" {
		t.Fatalf("request: client=%q body=%q", gotClient, gotBody)
	}
	cur, err := c.EvalRunStatus(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Eval != "bob/sql-safety" || cur.Counted == nil || *cur.Counted || len(cur.Integrity) != 1 || !cur.Integrity[0].Serious {
		t.Fatalf("status = %+v", cur)
	}
}

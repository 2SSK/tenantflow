// Command loadtest drives the REAL API end-to-end: it provisions N tenants
// (and optionally soft-deletes them again), polling each tenant until the
// workflow reaches its terminal-ish state, and prints measured latency
// distributions. It only reports what it actually observed — no simulated
// numbers.
//
// Usage:
//
//	TOKEN=$(curl -fsS -d grant_type=password -d client_id=tenantflow-api \
//	  -d client_secret=api-secret-123 -d username=<user> -d password=<pass> \
//	  http://localhost:8081/realms/tenantflow/protocol/openid-connect/token \
//	  | jq -r .access_token)
//	go run ./cmd/loadtest -url http://localhost:9090 -token "$TOKEN" \
//	  -n 100 -c 10 -prefix lt
//
// The -delete phase requires the API to have been started with a short
// TENANTFLOW_DELETE_GRACE_PERIOD (e.g. "2s") so soft deletes finish quickly.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type runResult struct {
	tenantID    string
	provision   time.Duration // POST -> status active
	provisionOK bool
	deletion    time.Duration // DELETE -> status deleted
	deletionOK  bool
	err         string
}

func main() {
	url := flag.String("url", "http://localhost:9090", "API base URL")
	token := flag.String("token", os.Getenv("TENANTFLOW_LOAD_TOKEN"), "admin bearer token (or env TENANTFLOW_LOAD_TOKEN)")
	n := flag.Int("n", 100, "number of tenants")
	c := flag.Int("c", 10, "concurrency")
	prefix := flag.String("prefix", "lt", "tenant ID prefix")
	mode := flag.String("mode", "shared", "isolation mode: shared or dedicated")
	dodelete := flag.Bool("delete", false, "run the soft-delete phase too")
	timeout := flag.Duration("timeout", 3*time.Minute, "per-tenant poll timeout")
	flag.Parse()

	if *mode != "shared" && *mode != "dedicated" {
		fmt.Fprintln(os.Stderr, "-mode must be 'shared' or 'dedicated'")
		os.Exit(2)
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "missing -token (or TENANTFLOW_LOAD_TOKEN)")
		os.Exit(2)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	auth := "Bearer " + *token

	// --- provision phase -------------------------------------------------
	results := make([]runResult, *n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, *c)
	start := time.Now()

	for i := 0; i < *n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			id := fmt.Sprintf("%s-%05d", *prefix, i)
			run := runResult{tenantID: id}

			t0 := time.Now()
			if err := postJSON(client, *url+"/api/v1/tenants", auth,
				map[string]string{"tenantID": id, "isolationMode": *mode}, nil); err != nil {
				run.err = "provision POST: " + err.Error()
				results[i] = run
				return
			}
			status, err := pollTenant(client, *url, id, *timeout, "active")
			run.provision = time.Since(t0)
			run.provisionOK = err == nil && status == "active"
			if err != nil {
				run.err = "provision poll: " + err.Error()
			} else if status != "active" {
				run.err = "provision ended at " + status
			}
			results[i] = run
		}(i)
	}
	wg.Wait()
	provisionWall := time.Since(start)

	// --- delete phase ----------------------------------------------------
	var deleteWall time.Duration
	if *dodelete {
		start = time.Now()
		for i := range results {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				run := &results[i]
				if !run.provisionOK {
					return
				}
				t0 := time.Now()
				req, _ := http.NewRequest(http.MethodDelete,
					*url+"/api/v1/tenants/"+run.tenantID, nil)
				req.Header.Set("Authorization", auth)
				resp, err := client.Do(req)
				if err != nil {
					run.deletionOK, run.deletion = false, 0
					run.err = "delete POST: " + err.Error()
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusAccepted {
					run.err = fmt.Sprintf("delete POST: status %d", resp.StatusCode)
					return
				}
				status, err := pollTenant(client, *url, run.tenantID, *timeout, "deleted")
				run.deletion = time.Since(t0)
				run.deletionOK = err == nil && status == "deleted"
				if err != nil {
					run.err = "delete poll: " + err.Error()
				} else if status != "deleted" {
					run.err = "delete ended at " + status
				}
			}(i)
		}
		wg.Wait()
		deleteWall = time.Since(start)
	}

	// --- DLQ check -------------------------------------------------------
	failedRuns := 0
	req, _ := http.NewRequest(http.MethodGet, *url+"/api/v1/failed-runs", nil)
	req.Header.Set("Authorization", auth)
	if resp, err := client.Do(req); err == nil {
		var body struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
			failedRuns = len(body.Runs)
		}
		resp.Body.Close()
	}

	// --- report -----------------------------------------------------------
	provisionOK, provisionErr := 0, 0
	var provLat []float64
	for _, r := range results {
		if r.provisionOK {
			provisionOK++
			provLat = append(provLat, float64(r.provision.Milliseconds())/1000)
		} else if r.err != "" {
			provisionErr++
		}
	}
	fmt.Printf("\nprovision: %d/%d ok, %d errored\n", provisionOK, *n, provisionErr)
	fmt.Printf("  wall: %s  throughput: %.1f tenants/min\n", provisionWall,
		float64(provisionOK)/provisionWall.Minutes())
	fmt.Printf("  provision->active latency (s): %s\n", summarize(provLat))

	if *dodelete {
		deleteOK, deleteErr := 0, 0
		var delLat []float64
		for _, r := range results {
			if r.deletionOK {
				deleteOK++
				delLat = append(delLat, float64(r.deletion.Milliseconds())/1000)
			} else if r.err != "" && r.provisionOK {
				deleteErr++
			}
		}
		fmt.Printf("\ndelete: %d/%d ok, %d errored\n", deleteOK, *n, deleteErr)
		fmt.Printf("  wall: %s  throughput: %.1f tenants/min\n", deleteWall,
			float64(deleteOK)/deleteWall.Minutes())
		fmt.Printf("  delete->deleted latency (s): %s\n", summarize(delLat))
	}

	fmt.Printf("\nfailed-runs (DLQ rows): %d\n", failedRuns)
	for _, r := range results {
		if r.err != "" {
			fmt.Printf("  error %s: %s\n", r.tenantID, r.err)
		}
	}
}

// postJSON sends an authenticated JSON POST and requires 2xx.
func postJSON(client *http.Client, apiURL, auth string, payload, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// pollTenant GETs the tenant detail until it reaches want (or "failed" — a
// terminal bad state for both phases) or the timeout expires. The two phases
// want different outcomes: provision waits for "active", delete waits for
// "deleted". Treating "active" as universal terminal made the delete phase
// return instantly (the tenant is still active during its grace period) and
// report every deletion as failed before the workflow even started.
func pollTenant(client *http.Client, apiURL, id string, timeout time.Duration, want string) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(apiURL + "/api/v1/tenants/" + id)
		if err != nil {
			return "", err
		}
		var t struct {
			Status string `json:"status"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&t)
		resp.Body.Close()
		if decodeErr != nil {
			return "", decodeErr
		}
		if t.Status == want || t.Status == "failed" {
			return t.Status, nil
		}
		if time.Now().After(deadline) {
			return t.Status, fmt.Errorf("timeout waiting (status %q)", t.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// summarize returns p50/p95/p99/mean/min/max for latencies in seconds.
func summarize(lat []float64) string {
	if len(lat) == 0 {
		return "n/a"
	}
	sort.Float64s(lat)
	pct := func(p float64) float64 {
		idx := int(p/100*float64(len(lat))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(lat) {
			idx = len(lat) - 1
		}
		return lat[idx]
	}
	var sum float64
	for _, v := range lat {
		sum += v
	}
	return fmt.Sprintf("p50=%.2f p95=%.2f p99=%.2f mean=%.2f min=%.2f max=%.2f (n=%d)",
		pct(50), pct(95), pct(99), sum/float64(len(lat)), lat[0], lat[len(lat)-1], len(lat))
}

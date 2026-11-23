package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func pt(min int, ms float64, origin string) model.PingPoint {
	return model.PingPoint{At: time.Unix(1_700_006_400, 0).Add(time.Duration(min) * time.Minute).UTC(), Origin: origin,
		RTT: time.Duration(ms * float64(time.Millisecond)), OK: ms >= 0}
}

func TestBucketPings(t *testing.T) {
	in := []model.PingPoint{pt(0, 1, "gw"), pt(5, 9, "gw"), pt(10, 2, "gw"), pt(15, -1, "gw"), pt(35, -1, "gw"), pt(40, 4, "netmon")}
	raw := bucketPings(in, 0)
	if len(raw) != 6 || *raw[1].Ms != 9 || raw[3].Ms != nil || raw[3].Missed != 1 {
		t.Fatalf("raw = %+v", raw)
	}
	b := bucketPings(in, 30*time.Minute)
	if len(b) != 3 || *b[0].Ms != 2 || *b[0].Min != 1 || *b[0].Max != 9 || b[0].Missed != 1 || b[0].Runs != 4 {
		t.Fatalf("buckets = %+v", b)
	}
	if b[1].Ms != nil || b[1].Origin != "gw" || b[2].Origin != "netmon" {
		t.Fatalf("origins mixed: %+v", b)
	}
}

func TestSummarizePings(t *testing.T) {
	s := summarizePings([]model.PingPoint{pt(0, 1, ""), pt(1, 2, ""), pt(2, 9, ""), pt(3, -1, "")})
	if s.Runs != 4 || *s.Median != 2 || *s.Min != 1 || *s.Max != 9 || s.Answered != 75 {
		t.Fatalf("summary = %+v", s)
	}
	if s := summarizePings(nil); s.Median != nil || s.Runs != 0 {
		t.Fatalf("empty = %+v", s)
	}
}

func TestPingsAPI(t *testing.T) {
	h, st, _ := newTest(t, nil)
	ctx := context.Background()
	for i := range 60 {
		st.RecordPings(ctx, "lan", "gw", time.Now().Add(time.Duration(i-60)*time.Minute/2), []model.PingSample{{Key: "mac:aa:00:00:00:00:01", RTT: 3 * time.Millisecond}})
	}
	q := "network=lan&key=mac:aa:00:00:00:00:01"
	var got struct {
		Points  []map[string]any
		Summary map[string]any
	}
	rr := do(h, "GET", "/api/pings?range=24h&"+q, "", false)
	if json.Unmarshal(rr.Body.Bytes(), &got); rr.Code != 200 || len(got.Points) != 60 || got.Summary["answered"].(float64) != 100 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if rr := do(h, "GET", "/api/pings?range=9y&"+q, "", false); rr.Code != 400 {
		t.Fatalf("bad range: %d", rr.Code)
	}
	if rr := do(h, "GET", "/api/pings?network=lan&key=none", "", false); !strings.Contains(rr.Body.String(), `"points":[]`) {
		t.Fatalf("empty: %s", rr.Body)
	}
	var page struct {
		Rows []map[string]any
		Next string
	}
	json.Unmarshal(do(h, "GET", "/api/pings/list?"+q, "", false).Body.Bytes(), &page)
	if len(page.Rows) != 50 || page.Next == "" {
		t.Fatalf("page 1: %d rows, next %q", len(page.Rows), page.Next)
	}
	next := page.Next
	page.Next = ""
	json.Unmarshal(do(h, "GET", "/api/pings/list?"+q+"&before="+next, "", false).Body.Bytes(), &page)
	if len(page.Rows) != 10 || page.Next != "" {
		t.Fatalf("page 2: %d rows, next %q", len(page.Rows), page.Next)
	}
	if rr := do(h, "GET", "/api/pings/list?"+q+"&before=x", "", false); rr.Code != 400 {
		t.Fatalf("bad cursor: %d", rr.Code)
	}
	if body := do(h, "GET", "/device?"+q, "", false).Body.String(); !strings.Contains(body, "data-ping-history") {
		t.Fatal("no ping history on the device page")
	}
}

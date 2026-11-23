package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func TestJobsPage(t *testing.T) {
	h, st, _ := newTest(t, creds)
	ctx := context.Background()
	now := time.Now().UTC()
	for range 100 {
		st.AddJobRun(ctx, model.JobRun{Network: "lan", Kind: model.RunPresence, Source: "nmap", Trigger: "scheduled", At: now, Outcome: model.OutcomeOK, Message: "24 hosts seen"})
	}
	st.AddJobRun(ctx, model.JobRun{Network: "lan", Kind: model.RunDevice, Source: "services 10.0.0.1", Key: nasKey, Trigger: "manual", At: now,
		Duration: 90 * time.Second, Outcome: model.OutcomeFailed, Message: "nmap: <boom>"})

	body := do(h, "GET", "/jobs", "", true).Body.String()
	for _, want := range []string{"24 hosts seen", "nmap: &lt;boom&gt;", "1m30s", "manual", `href="/device?key=mac`, "nas", "Older", `href="/jobs" aria-current="page"`, `id="queue"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	body = do(h, "GET", "/jobs?outcome=failed&kind=device&network=lan", "", true).Body.String()
	if strings.Contains(body, "24 hosts") || !strings.Contains(body, "boom") || strings.Contains(body, "Older") {
		t.Error("filters not applied")
	}
	body = do(h, "GET", "/jobs?kind=presence&before=2", "", true).Body.String()
	if !strings.Contains(body, "24 hosts") || !strings.Contains(body, `href="/jobs?kind=presence">← Newest`) {
		t.Error("paging")
	}
	if body = do(h, "GET", "/jobs?network=none", "", true).Body.String(); !strings.Contains(body, "No finished jobs match") {
		t.Error("empty state")
	}
}

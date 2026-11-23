package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtrig/netmon/internal/model"
)

func TestJobRuns(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	runs := []model.JobRun{
		{Network: "lan", Kind: model.RunPresence, Source: "nmap", Trigger: "scheduled", At: now.Add(-48 * time.Hour), Outcome: model.OutcomeOK},
		{Network: "lan", Kind: model.RunDevice, Source: "services 10.0.0.2", Key: "mac:aa", Trigger: "manual", At: now, Duration: 1500 * time.Millisecond, Outcome: model.OutcomeFailed, Message: "boom"},
		{Network: "iot", Kind: model.RunPing, At: now, Outcome: model.OutcomeOK},
	}
	for _, r := range runs {
		if err := s.AddJobRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.JobRuns(ctx, JobRunFilter{})
	if err != nil || len(all) != 3 || all[0].Network != "iot" {
		t.Fatalf("%+v %v", all, err)
	}
	runs[1].ID = all[1].ID
	if all[1] != runs[1] {
		t.Fatalf("got %+v", all[1])
	}
	for _, c := range []struct {
		f    JobRunFilter
		want int
	}{{JobRunFilter{Network: "lan"}, 2}, {JobRunFilter{Kind: "ping"}, 1}, {JobRunFilter{Outcome: "failed", Network: "lan"}, 1},
		{JobRunFilter{Before: all[0].ID}, 2}, {JobRunFilter{Limit: 1}, 1}} {
		if got, _ := s.JobRuns(ctx, c.f); len(got) != c.want {
			t.Errorf("%+v: %d runs", c.f, len(got))
		}
	}
	s.PruneJobRuns(ctx, now.Add(-time.Hour))
	s.RenameNetwork(ctx, "lan", "home")
	if got, _ := s.JobRuns(ctx, JobRunFilter{Network: "home"}); len(got) != 1 {
		t.Fatalf("after prune and rename: %+v", got)
	}
	s.DeleteNetworkData(ctx, "home")
	if got, _ := s.JobRuns(ctx, JobRunFilter{}); len(got) != 1 {
		t.Fatalf("after delete: %+v", got)
	}
}

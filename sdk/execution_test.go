package sdk

import (
	"testing"
	"time"
)

// AG-09: ExecutionStore caps at 500 entries; oldest is evicted.
func TestExecutionStore_Cap500(t *testing.T) {
	s := NewExecutionStore()
	for i := 0; i < 501; i++ {
		s.Create("exec_"+itoa(i), "sid", AgentClaude, "p", func() {})
	}
	list := s.List()
	if len(list) > 500 {
		t.Fatalf("ExecutionStore size = %d, want <= 500", len(list))
	}
	// The first execution (exec_0) should have been evicted (it's the oldest).
	if s.Get("exec_0") != nil {
		t.Fatalf("oldest execution not evicted (exec_0 still present)")
	}
	// The last execution should be present.
	if s.Get("exec_500") == nil {
		t.Fatalf("newest execution missing (exec_500)")
	}
}

// AG-08: timeout is capped at 120 minutes. This is verified at the handler
// level (handleAgentSendPrompt caps timeoutMin to 120). Here we test that the
// execution store correctly tracks status transitions.
func TestExecutionStore_StatusTransitions(t *testing.T) {
	s := NewExecutionStore()
	e := s.Create("exec_1", "sid", AgentClaude, "p", func(){})
	if e.Status != ExecutionRunning {
		t.Fatalf("initial status = %s, want running", e.Status)
	}
	s.AppendMessage("exec_1", Message{Type: MessageTypeText, Content: "hi"})
	s.Complete("exec_1")
	if e.Status != ExecutionCompleted {
		t.Fatalf("after complete: status = %s, want completed", e.Status)
	}

	e2 := s.Create("exec_2", "sid", AgentClaude, "p", func(){})
	s.Fail("exec_2", "boom")
	if e2.Status != ExecutionError {
		t.Fatalf("after fail: status = %s, want error", e2.Status)
	}
	if e2.Error != "boom" {
		t.Fatalf("error msg = %q, want boom", e2.Error)
	}

	e3 := s.Create("exec_3", "sid", AgentClaude, "p", func(){})
	s.Cancel("exec_3")
	if e3.Status != ExecutionCancelled {
		t.Fatalf("after cancel: status = %s, want cancelled", e3.Status)
	}
}

// GetBySession returns the latest execution for a session.
func TestExecutionStore_GetBySession(t *testing.T) {
	s := NewExecutionStore()
	s.Create("exec_1", "sid-A", AgentClaude, "p1", func(){})
	time.Sleep(1 * time.Millisecond)
	s.Create("exec_2", "sid-A", AgentClaude, "p2", func(){})
	s.Create("exec_3", "sid-B", AgentClaude, "p3", func(){})

	latest := s.GetBySession("sid-A")
	if latest == nil || latest.ID != "exec_2" {
		t.Fatalf("GetBySession(sid-A) = %v, want exec_2", latest)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

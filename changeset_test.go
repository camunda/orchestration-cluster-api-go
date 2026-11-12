package camunda_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	camunda "github.com/camunda/orchestration-cluster-api-go/v11"
)

// captureUpdateUserTask sends body with UpdateUserTask to a stub server and returns
// the decoded JSON request body it received.
func captureUpdateUserTask(t *testing.T, body camunda.UserTaskUpdateRequest) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v2/user-tasks/42" {
			t.Errorf("request = %s %s, want PATCH /v2/user-tasks/42", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client, err := camunda.New(camunda.WithRestAddress(srv.URL), camunda.WithLogLevel(camunda.LogOff))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.UpdateUserTask(context.Background(), camunda.MustUserTaskKey("42"), body); err != nil {
		t.Fatalf("UpdateUserTask: %v", err)
	}
	return got
}

// TestUpdateUserTaskChangesetDatesWireBody: the user task update clears a date with
// "" (null or omission preserves it), so Clear must reach the wire as "", a set date
// as RFC 3339, and an untouched date not at all. The last call wins.
func TestUpdateUserTaskChangesetDatesWireBody(t *testing.T) {
	due := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		build func(*camunda.Changeset)
		want  string
	}{
		{"clear due date", func(c *camunda.Changeset) { c.ClearDueDate() }, `{"dueDate":""}`},
		{"clear follow-up date", func(c *camunda.Changeset) { c.ClearFollowUpDate() }, `{"followUpDate":""}`},
		{"set due date", func(c *camunda.Changeset) { c.SetDueDate(due) }, `{"dueDate":"2026-01-02T03:04:05Z"}`},
		{"set follow-up date", func(c *camunda.Changeset) { c.SetFollowUpDate(due) }, `{"followUpDate":"2026-01-02T03:04:05Z"}`},
		{"unset preserves", func(c *camunda.Changeset) { c.SetPriority(80) }, `{"priority":80}`},
		{"null preserves", func(c *camunda.Changeset) { c.SetDueDateNil() }, `{"dueDate":null}`},
		{"set after clear", func(c *camunda.Changeset) { c.ClearDueDate(); c.SetDueDate(due) }, `{"dueDate":"2026-01-02T03:04:05Z"}`},
		{"clear after set", func(c *camunda.Changeset) { c.SetDueDate(due); c.ClearDueDate() }, `{"dueDate":""}`},
		{"unset after clear", func(c *camunda.Changeset) { c.ClearFollowUpDate(); c.UnsetFollowUpDate() }, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &camunda.Changeset{} // NewChangeset would also send the default priority (50)
			tc.build(c)
			req := camunda.NewUserTaskUpdateRequest()
			req.SetChangeset(*c)
			got := captureUpdateUserTask(t, *req)
			want := map[string]any{"changeset": mustJSON(t, tc.want)}
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				t.Errorf("request body = %s, want {\"changeset\":%s}", gotJSON, tc.want)
			}
		})
	}
}

// TestChangesetDateRoundTrip: every state of a changeset date survives
// marshal -> unmarshal: cleared ("" on the wire), set (RFC 3339), null, absent.
func TestChangesetDateRoundTrip(t *testing.T) {
	due := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	type dateAPI struct {
		prop    string
		set     func(*camunda.Changeset, time.Time)
		setNil  func(*camunda.Changeset)
		clear   func(*camunda.Changeset)
		cleared func(*camunda.Changeset) bool
		get     func(*camunda.Changeset) (*time.Time, bool)
	}
	for _, d := range []dateAPI{
		{"dueDate", (*camunda.Changeset).SetDueDate, (*camunda.Changeset).SetDueDateNil,
			(*camunda.Changeset).ClearDueDate, (*camunda.Changeset).IsDueDateCleared, (*camunda.Changeset).GetDueDateOk},
		{"followUpDate", (*camunda.Changeset).SetFollowUpDate, (*camunda.Changeset).SetFollowUpDateNil,
			(*camunda.Changeset).ClearFollowUpDate, (*camunda.Changeset).IsFollowUpDateCleared, (*camunda.Changeset).GetFollowUpDateOk},
	} {
		for _, st := range []struct {
			name        string
			build       func(*camunda.Changeset)
			wire        string
			wantCleared bool
			wantSet     bool
			wantValue   *time.Time
		}{
			{"cleared", d.clear, `{"` + d.prop + `":""}`, true, false, nil},
			{"set", func(c *camunda.Changeset) { d.set(c, due) }, `{"` + d.prop + `":"2026-01-02T03:04:05Z"}`, false, true, &due},
			{"null", d.setNil, `{"` + d.prop + `":null}`, false, true, nil},
			{"absent", func(*camunda.Changeset) {}, `{}`, false, false, nil},
		} {
			t.Run(d.prop+"/"+st.name, func(t *testing.T) {
				src := &camunda.Changeset{}
				src.SetCandidateUsers([]string{"alice"})
				src.AdditionalProperties = map[string]any{"custom": "x"}
				st.build(src)
				b, err := json.Marshal(src)
				if err != nil {
					t.Fatalf("Marshal: %v", err)
				}
				want := mustJSON(t, st.wire)
				want["candidateUsers"] = []any{"alice"}
				want["custom"] = "x"
				var got map[string]any
				_ = json.Unmarshal(b, &got)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("wire = %s, want %v", b, want)
				}

				var back camunda.Changeset
				if err := json.Unmarshal(b, &back); err != nil {
					t.Fatalf("Unmarshal %s: %v", b, err)
				}
				v, ok := d.get(&back)
				if d.cleared(&back) != st.wantCleared || ok != st.wantSet ||
					(st.wantValue == nil) != (v == nil) || (v != nil && !v.Equal(*st.wantValue)) {
					t.Errorf("decoded: cleared=%v set=%v value=%v; want cleared=%v set=%v value=%v",
						d.cleared(&back), ok, v, st.wantCleared, st.wantSet, st.wantValue)
				}
				if !reflect.DeepEqual(back.CandidateUsers, []string{"alice"}) {
					t.Errorf("candidateUsers = %v, want [alice]", back.CandidateUsers)
				}
				if !reflect.DeepEqual(back.AdditionalProperties, map[string]any{"custom": "x"}) {
					t.Errorf("additional properties = %v, want only custom (a cleared date is not one)",
						back.AdditionalProperties)
				}
				again, _ := json.Marshal(&back)
				if string(again) != string(b) {
					t.Errorf("re-marshal = %s, want %s", again, b)
				}
			})
		}
	}
}

// TestChangesetDecodeReplacesLikeGeneratedDecoder: Changeset keeps the generated
// decoder, which replaces the whole value; a clear absent from the input is reset
// with everything else, and a timestamp or null replaces one.
func TestChangesetDecodeReplacesLikeGeneratedDecoder(t *testing.T) {
	for _, in := range []string{`{}`, `{"priority":80}`, `{"dueDate":"2027-05-06T07:08:09Z"}`, `{"dueDate":null}`} {
		t.Run(in, func(t *testing.T) {
			c := &camunda.Changeset{}
			c.ClearDueDate()
			if err := json.Unmarshal([]byte(in), c); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if c.IsDueDateCleared() {
				t.Errorf("decoding %s kept a clear the input does not carry", in)
			}
		})
	}
	var c camunda.Changeset
	if err := json.Unmarshal([]byte("{ \"dueDate\" :\n \"\" , \"followUpDate\" : null }"), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !c.IsDueDateCleared() || c.IsFollowUpDateCleared() || !c.HasFollowUpDate() {
		t.Errorf("dueCleared=%v followUpCleared=%v followUpSet=%v; want a due-date clear and a follow-up null",
			c.IsDueDateCleared(), c.IsFollowUpDateCleared(), c.HasFollowUpDate())
	}
}

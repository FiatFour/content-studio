package jobs

import "testing"

func TestAssessResume(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name string
		in   ResumeInput
		want ResumeVerdict
		why  string
	}{
		{"running", ResumeInput{State: Running}, ResumeNotApplicable, "not_failed"},
		{"no class", ResumeInput{State: Failed}, ResumeUnknown, "no_error_class"},
		{"unclassified", ResumeInput{State: Failed, ErrorClass: s("mystery")}, ResumeUnknown, "unknown_error_class"},
		{"uncertain", ResumeInput{State: Failed, ErrorClass: s("uncertain_outcome")}, ResumeUnsafe, "uncertain_outcome"},
		{"rewrite", ResumeInput{State: Failed, ErrorClass: s("script_duration")}, ResumeSafe, "rewrite_run"},
		{"rewritten", ResumeInput{State: Failed, ErrorClass: s("script_duration"), AlreadyRewritten: true}, ResumeUnsafe, "already_rewritten"},
		{"first stage", ResumeInput{State: Failed, ErrorClass: s("internal")}, ResumeSafe, "upstream_recorded"},
		{"recorded", ResumeInput{State: Failed, ErrorClass: s("internal"), UpstreamTotal: 2, UpstreamCompleted: 2, UpstreamRecorded: 2}, ResumeSafe, "upstream_recorded"},
		{"unrecorded", ResumeInput{State: Failed, ErrorClass: s("internal"), UpstreamTotal: 2, UpstreamCompleted: 2, UpstreamRecorded: 1}, ResumeUnknown, "no_execution_record"},
		{"incomplete", ResumeInput{State: Failed, ErrorClass: s("internal"), UpstreamTotal: 2, UpstreamCompleted: 1, UpstreamRecorded: 1}, ResumeUnknown, "upstream_incomplete"},
	}
	for _, c := range cases {
		got := AssessResume(c.in)
		if got.Verdict != c.want || got.Reason != c.why {
			t.Errorf("%s: got %+v, want %s/%s", c.name, got, c.want, c.why)
		}
	}
	for _, c := range AllErrorClasses() {
		cl := string(c)
		if AssessResume(ResumeInput{State: Failed, ErrorClass: &cl}).Verdict == ResumeNotApplicable {
			t.Errorf("%s: no verdict", c)
		}
	}
}

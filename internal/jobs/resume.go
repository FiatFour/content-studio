package jobs

// ResumeVerdict answers "can this failed job safely resume" from recorded data only.
type ResumeVerdict string

const (
	ResumeNotApplicable ResumeVerdict = "not_applicable" // the job is not failed, so nothing to resume
	ResumeSafe          ResumeVerdict = "safe"
	ResumeUnsafe        ResumeVerdict = "unsafe"
	ResumeUnknown       ResumeVerdict = "unknown"
)

// ResumeInput is the recorded evidence about one job. Nothing here is guessed.
type ResumeInput struct {
	State      State
	ErrorClass *string
	// UpstreamTotal counts the jobs before this one in the run; UpstreamCompleted how many are completed;
	// UpstreamRecorded how many completed ones have a stage_executions row (run + stage).
	UpstreamTotal, UpstreamCompleted, UpstreamRecorded int
	// AlreadyRewritten is true when a rewrite run already continues this run.
	AlreadyRewritten bool
}

// ResumeAssessment is the verdict plus a short machine reason the UI translates.
type ResumeAssessment struct {
	Verdict ResumeVerdict `json:"verdict"`
	Reason  string        `json:"reason"`
}

// AssessResume mirrors what Queue.Resume would do and says whether doing it re-runs only the failed
// stage on inputs that are known to be intact. It answers unknown whenever the data cannot show that.
func AssessResume(in ResumeInput) ResumeAssessment {
	if in.State != Failed {
		return ResumeAssessment{ResumeNotApplicable, "not_failed"}
	}
	if in.ErrorClass == nil || *in.ErrorClass == "" {
		return ResumeAssessment{ResumeUnknown, "no_error_class"}
	}
	class := ErrorClass(*in.ErrorClass)
	if _, known := remedies[class]; !known {
		return ResumeAssessment{ResumeUnknown, "unknown_error_class"}
	}
	switch {
	case class == ErrUncertainOutcome:
		// The request may have been accepted and billed; running it again could repeat it.
		return ResumeAssessment{ResumeUnsafe, "uncertain_outcome"}
	case class.Remedy() == RewriteScript:
		if in.AlreadyRewritten {
			return ResumeAssessment{ResumeUnsafe, "already_rewritten"}
		}
		return ResumeAssessment{ResumeSafe, "rewrite_run"}
	}
	if in.UpstreamCompleted < in.UpstreamTotal {
		return ResumeAssessment{ResumeUnknown, "upstream_incomplete"}
	}
	if in.UpstreamRecorded < in.UpstreamTotal {
		return ResumeAssessment{ResumeUnknown, "no_execution_record"}
	}
	return ResumeAssessment{ResumeSafe, "upstream_recorded"}
}

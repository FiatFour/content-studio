package pipeline

import (
	"context"

	"github.com/fiat/content-studio/internal/artifacts"
)

// HistoryRulesForTest loads a stored history script with its sources through the QA loader and runs
// the History rules with the given measured length, subtitle text and still metadata.
func (d *Deps) HistoryRulesForTest(ctx context.Context, scriptArt artifacts.Artifact, e Episode, durS float64, subs string, stills []map[string]any) ([]QAResult, error) {
	in, err := d.loadHistoryQA(ctx, scriptArt)
	if err != nil {
		return nil, err
	}
	in.stills = stills
	return historyRules(in, e, durS, subs, historyMysterySpec), nil
}

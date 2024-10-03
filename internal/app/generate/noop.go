package generate

import (
	"context"

	"github.com/prometheus/prometheus/model/rulefmt"
	"github.com/slok/sloth/internal/alert"
	"github.com/slok/sloth/internal/info"
	"github.com/slok/sloth/internal/prometheus"
)

type noopSLIRecordingRulesGenerator bool

const NoopSLIRecordingRulesGenerator = noopSLIRecordingRulesGenerator(false)

func (noopSLIRecordingRulesGenerator) GenerateSLIRecordingRules(_ context.Context, _ prometheus.SLO, _ alert.MWMBAlertGroup) ([]rulefmt.Rule, error) {
	return nil, nil
}

type noopMetadataRecordingRulesGenerator bool

const NoopMetadataRecordingRulesGenerator = noopMetadataRecordingRulesGenerator(false)

func (noopMetadataRecordingRulesGenerator) GenerateMetadataRecordingRules(_ context.Context, _ info.Info, _ prometheus.SLO, _ alert.MWMBAlertGroup) ([]rulefmt.Rule, error) {
	return nil, nil
}

type noopSLOAlertRulesGenerator bool

const NoopSLOAlertRulesGenerator = noopSLOAlertRulesGenerator(false)

func (noopSLOAlertRulesGenerator) GenerateSLOAlertRules(_ context.Context, _ prometheus.SLO, _ alert.MWMBAlertGroup) ([]rulefmt.Rule, error) {
	return nil, nil
}

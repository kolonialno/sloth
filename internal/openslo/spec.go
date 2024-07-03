package openslo

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	openslov1 "github.com/OpenSLO/oslo/pkg/manifest/v1"
	"github.com/slok/sloth/internal/prometheus"
	"gopkg.in/yaml.v2"
)

var (
	MultiDimensionSliEnabledAnnotation         = "multi-dimensional-sli.openslo.com/enabled"
	MultiDimensionSliSecondDimensionAnnotation = "multi-dimensional-sli.openslo.com/second-dimension"
)

var (
	multiDimensionSliEnabled         = false
	multiDimensionSliSecondDimension = ""
)

type YAMLSpecLoader struct {
	windowPeriod time.Duration
}

// YAMLSpecLoader knows how to load YAML specs and converts them to a model.
func NewYAMLSpecLoader(windowPeriod time.Duration) YAMLSpecLoader {
	return YAMLSpecLoader{
		windowPeriod: windowPeriod,
	}
}

var (
	specTypeV1RegexKind       = regexp.MustCompile(`(?m)^kind: +['"]?SLO['"]? *$`)
	specTypeV1RegexAPIVersion = regexp.MustCompile(`(?m)^apiVersion: +['"]?openslo\/v1['"]? *$`)
)

func (y YAMLSpecLoader) IsSpecType(ctx context.Context, data []byte) bool {
	// return specTypeV1AlphaRegexKind.Match(data) && specTypeV1AlphaRegexAPIVersion.Match(data)
	return specTypeV1RegexKind.Match(data) && specTypeV1RegexAPIVersion.Match(data)
}

func (y YAMLSpecLoader) LoadSpec(ctx context.Context, data []byte) (*prometheus.SLOGroup, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("spec is required")
	}

	s := openslov1.SLO{}
	err := yaml.Unmarshal(data, &s)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshall YAML spec correctly: %w", err)
	}

	// Check version.
	if s.APIVersion != openslov1.APIVersion {
		return nil, fmt.Errorf("invalid spec version, should be %q", openslov1.APIVersion)
	}

	// Check at least we have one SLO.
	if len(s.Spec.Objectives) == 0 {
		return nil, fmt.Errorf("at least one SLO is required")
	}

	// Validate time windows are correct.
	err = y.validateTimeWindow(s)
	if err != nil {
		return nil, fmt.Errorf("invalid SLO time windows: %w", err)
	}

	mdse, ok := s.Metadata.Annotations[MultiDimensionSliEnabledAnnotation]
	if ok {
		mdseb, err := strconv.ParseBool(mdse)
		if err != nil {
			return nil, fmt.Errorf("unable to parse multi dimension sli annotation")
		}
		multiDimensionSliEnabled = mdseb
		mdssd, okay := s.Metadata.Annotations[MultiDimensionSliSecondDimensionAnnotation]
		if !okay {
			return nil, fmt.Errorf("second dimension not present for multi-dimensions slis")
		} else {
			multiDimensionSliSecondDimension = mdssd
		}
	}

	m, err := y.mapSpecToModel(s)
	if err != nil {
		return nil, fmt.Errorf("could not map to model: %w", err)
	}

	return m, nil
}

func (y YAMLSpecLoader) mapSpecToModel(spec openslov1.SLO) (*prometheus.SLOGroup, error) {
	slos, err := y.getSLOs(spec)
	if err != nil {
		return nil, fmt.Errorf("could not map SLOs correctly: %w", err)
	}

	return &prometheus.SLOGroup{SLOs: slos}, nil
}

var (
	durationRegex       = regexp.MustCompile(`(\d+)([wdhsm])`)
	durationLengthRegex = regexp.MustCompile(`(\d+)`)
	durationWindowRegex = regexp.MustCompile(`([wdhsm])`)
)

// validateTimeWindow will validate that Sloth only supports 30 day based time windows
// we need this because time windows are a required by OpenSLO.
func (YAMLSpecLoader) validateTimeWindow(spec openslov1.SLO) error {
	if len(spec.Spec.TimeWindow) == 0 {
		return nil
	}

	if len(spec.Spec.TimeWindow) > 1 {
		return fmt.Errorf("only 1 time window is supported")
	}

	if !spec.Spec.TimeWindow[0].IsRolling {
		return fmt.Errorf("must be rolling window")
	}

	if !durationRegex.MatchString(spec.Spec.TimeWindow[0].Duration) {
		return fmt.Errorf("the duration string is not confirming")
	}

	t := spec.Spec.TimeWindow[0]
	durationStrings := durationWindowRegex.FindString(t.Duration)
	if durationStrings != "d" {
		return fmt.Errorf("only days based time windows are supported")
	}

	return nil
}

var multiSliTpl = template.Must(template.New("").Parse(`label_join(label_join(max_over_time({{ .query }}), 'sloth_slo', '-', 'sloth_slo', '{{ .second_label_identifier }}'), 'sloth_id', '-', 'sloth_service', 'sloth_slo'`))

var errorRatioRawQueryTpl = template.Must(template.New("").Parse(`
  1 - (
    (
      {{ .good }}
    )
    /
    (
      {{ .total }}
    )
  )
`))

const metricSourceSpecQueryKey = "query"

// getSLI gets the SLI from the OpenSLO slo objective, we only support ratio based openSLO objectives,
// however we will convert to a raw based sloth SLI because the ratio queries that we have differ from
// Sloth. Sloth uses bad/total events, OpenSLO uses good/total events. We get the ratio using good events
// and then rest to 1, to get a raw error ratio query.
func (y YAMLSpecLoader) getSLI(spec openslov1.SLOSpec, slo openslov1.Objective) (*prometheus.SLI, error) {
	if slo.Target == 0.0 {
		return nil, fmt.Errorf("missing target")
	}

	if spec.Indicator == nil {
		return nil, fmt.Errorf("missing inline sli")
	}

	sli := spec.Indicator
	if sli.Spec.RatioMetric != nil && sli.Spec.ThresholdMetric != nil {
		return nil, fmt.Errorf("missing ratioMetric and/or thresholdMetric. One and only one must be supplied")
	}

	mdse, ok := sli.Metadata.Annotations[MultiDimensionSliEnabledAnnotation]
	if ok && (strings.ToLower(mdse) == "true") {
		_, okay := sli.Metadata.Annotations[MultiDimensionSliSecondDimensionAnnotation]
		if !okay {
			return nil, fmt.Errorf("second dimension not present for multi-dimensions slis")
		}
	}

	if sli.Spec.RatioMetric != nil {
		// Ratio Metrics
		good := sli.Spec.RatioMetric.Good
		// bad := sli.Spec.RatioMetric.Bad
		total := sli.Spec.RatioMetric.Total

		if good.MetricSource.Type != "prometheus" && good.MetricSource.Type != "sloth" {
			return nil, fmt.Errorf("prometheus or sloth query ratio 'good' source is required")
		}

		// if bad.MetricSource.Type != "prometheus" && bad.MetricSource.Type != "sloth" {
		// 	return nil, fmt.Errorf("prometheus or sloth query ratio 'bad' source is required")
		// }

		if total.MetricSource.Type != "prometheus" && total.MetricSource.Type != "sloth" {
			return nil, fmt.Errorf("prometheus or sloth query ratio 'total' source is required")
		}

		// if good.QueryType != "promql" {
		// 	return nil, fmt.Errorf("unsupported 'good' indicator query type: %s", good.QueryType)
		// }

		// if bad.QueryType != "promql" {
		// 	return nil, fmt.Errorf("unsupported 'bad' indicator query type: %s", bad.QueryType)
		// }

		// if total.QueryType != "promql" {
		// 	return nil, fmt.Errorf("unsupported 'total' indicator query type: %s", total.QueryType)
		// }

		goodQuery := good.MetricSource.MetricSourceSpec[metricSourceSpecQueryKey]
		// badQuery := bad.MetricSource.MetricSourceSpec[metricSourceSpecQueryKey]
		totalQuery := total.MetricSource.MetricSourceSpec[metricSourceSpecQueryKey]

		// Map as good and total events as a raw query.
		var b bytes.Buffer
		err := errorRatioRawQueryTpl.Execute(&b, map[string]string{"good": goodQuery, "total": totalQuery})
		if err != nil {
			return nil, fmt.Errorf("could not execute mapping SLI template: %w", err)
		}

		if multiDimensionSliEnabled {
			var c bytes.Buffer

			err := multiSliTpl.Execute(&c, map[string]string{"query": b.String(), "second_label_identifier": multiDimensionSliSecondDimension})
			if err != nil {
				return nil, fmt.Errorf("could not execute multi-dimension-sli template: %w", err)
			}

			return &prometheus.SLI{Raw: &prometheus.SLIRaw{
				ErrorRatioQuery: c.String(),
			}}, nil

		}

		return &prometheus.SLI{Raw: &prometheus.SLIRaw{
			ErrorRatioQuery: b.String(),
		}}, nil
	}

	if sli.Spec.ThresholdMetric != nil {
		// Threshold Metrics
		threshold := sli.Spec.ThresholdMetric

		if threshold.MetricSource.Type != "prometheus" && threshold.MetricSource.Type != "sloth" {
			return nil, fmt.Errorf("prometheus or sloth query 'threshold' source is required")
		}

		// Ensure this gives you a straight percentage
		thresholdQuery := threshold.MetricSource.MetricSourceSpec[metricSourceSpecQueryKey]

		if multiDimensionSliEnabled {
			var c bytes.Buffer

			err := multiSliTpl.Execute(&c, map[string]string{"query": thresholdQuery, "second_label_identifier": multiDimensionSliSecondDimension})
			if err != nil {
				return nil, fmt.Errorf("could not execute multi-dimension-sli template: %w", err)
			}

			return &prometheus.SLI{Raw: &prometheus.SLIRaw{
				ErrorRatioQuery: c.String(),
			}}, nil
		}

		return &prometheus.SLI{Raw: &prometheus.SLIRaw{
			ErrorRatioQuery: thresholdQuery,
		}}, nil
	}

	return nil, fmt.Errorf("unknown error")
}

// getSLOs will try getting all the objectives as individual SLOs, this way we can map
// to what Sloth understands as an SLO, that OpenSLO understands as a list of objectives
// for the same SLO.
func (y YAMLSpecLoader) getSLOs(spec openslov1.SLO) ([]prometheus.SLO, error) {
	res := []prometheus.SLO{}

	for idx, slo := range spec.Spec.Objectives {
		sli, err := y.getSLI(spec.Spec, slo)
		if err != nil {
			return nil, fmt.Errorf("could not map SLI: %w", err)
		}

		timeWindow := y.windowPeriod
		if len(spec.Spec.TimeWindow) > 0 {
			length := durationLengthRegex.FindString(spec.Spec.TimeWindow[0].Duration)
			timeWindowDuration, err := strconv.Atoi(length)
			if err != nil {
				return nil, fmt.Errorf("could not parse time window duration: %s", length)
			}
			timeWindow = time.Duration(timeWindowDuration) * 24 * time.Hour
		}

		// TODO(slok): Think about using `slo.Value` insted of idx (`slo.Value` is not mandatory).
		res = append(res, prometheus.SLO{
			ID:                               fmt.Sprintf("%s-%s-%d", spec.Spec.Service, spec.Metadata.Name, idx),
			Name:                             fmt.Sprintf("%s-%d", spec.Metadata.Name, idx),
			Service:                          spec.Spec.Service,
			Description:                      spec.Spec.Description,
			TimeWindow:                       timeWindow,
			SLI:                              *sli,
			Objective:                        slo.Target * 100, // OpenSLO uses ratios, we use percents.
			PageAlertMeta:                    prometheus.AlertMeta{Disable: true},
			TicketAlertMeta:                  prometheus.AlertMeta{Disable: true},
			MultiDimensionSliEnabled:         multiDimensionSliEnabled,
			MultiDimensionSliSecondDimension: multiDimensionSliSecondDimension,
		})
	}

	return res, nil
}

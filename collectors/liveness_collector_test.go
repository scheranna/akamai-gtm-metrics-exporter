package collectors

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"github.com/sirupsen/logrus"
	"gopkg.in/h2non/gock.v1"
)

const mockAkamaiBaseURL = "https://akaa-baseurl-xxxxxxxxxxx-xxxxxxxxxxxxx.luna.akamaiapis.net"

func init() {
    logrus.SetLevel(logrus.DebugLevel)
}

type livenessConstMetricCollector struct {
	collector *GTMLivenessTrafficExporter
}

// GTMLivenessTrafficExporter's real Describe() doesn't know about these const metrics (which might not exist in the current scrape),
// and CollectAndCompare() will fail if they are not described, so shim this into place.
func (c livenessConstMetricCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- prometheus.NewDesc(
		"akamai_property_liveness_errors_datacenter_failures",
		"Number of datacenter failures (per domain, property, datacenter)",
		[]string{"domain", "property", "datacenter"},
		nil,
	)
	ch <- prometheus.NewDesc(
		"akamai_property_liveness_errors_datacenter_failure_duration",
		"Datacenter failure duration (per domain, property, datacenter)",
		[]string{"domain", "property", "datacenter"},
		nil,
	)
}

func (c livenessConstMetricCollector) Collect(ch chan<- prometheus.Metric) {
	c.collector.Collect(ch)
}

// these maps are declared globally in liveness_collector.go, so reset them between tests
func resetLivenessCollectorTestState() {
	gtmLivenessTrafficExporter = GTMLivenessTrafficExporter{}
	livenessDurationHistogramMap = make(map[string]map[string]map[int]prometheus.Histogram)
	livenessErrorsSummaryMap = make(map[string]map[string]map[int]prometheus.Summary)
}

// Creates a collector and registry for a test, with a configurable tstart (the first LastTimestamp) and a single property.
func newTestLivenessCollector(tstart time.Time) (*GTMLivenessTrafficExporter, *prometheus.Registry) {
	resetLivenessCollectorTestState()

	registry := prometheus.NewRegistry()
	useTimestamp := false
	collector := NewLivenessTrafficCollector(
		context.Background(),
		mockV12Session(),
		registry,
		GTMMetricsConfig{
			Domains: []*DomainTraffic{
				{
					Name: "example.akadns.net",
					Liveness: []*LivenessTestConfig{
						{
							PropertyName: "www",
						},
					},
				},
			},
			UseTimestamp: &useTimestamp,
		},
		"akamai_",
		tstart,
		time.Hour,
	)

	return collector, registry
}

// mocks responses from the liveness-test reporting API for a single day based on the date passed in YYYY-MM-DD format
func mockLivenessCollectorResponsesSingleDay(t *testing.T, date string, body string) {
	t.Helper()

	gock.New(mockAkamaiBaseURL).
		Get("/gtm-api/v1/reports/liveness-tests/window").
		Reply(200).
		JSON(map[string]string{
			"start": date + "T00:00:00Z",
			"end":   date + "T23:59:59Z",
		})

	gock.New(mockAkamaiBaseURL).
		Get("/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www").
		MatchParam("date", date).
		Reply(200).
		BodyString(body)
}

func TestLivenessCollectorCollectAndCompareUsesLatestNewRowForConstMetrics(t *testing.T) {
	defer gock.Off()
	collector, _ := newTestLivenessCollector(time.Date(2026, time.August, 13, 12, 55, 0, 0, time.UTC))
	wrappedCollector := livenessConstMetricCollector{collector: collector}

	mockLivenessCollectorResponsesSingleDay(t, "2026-08-13", `{
		"metadata": {
			"date": "2026-08-13",
			"domain": "example.akadns.net",
			"property": "www",
			"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-13"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-13T13:00:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 15,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:01:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 60,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:01:05Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 120,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	// const metrics should report 1 for the counter and 120 for the duration since only the latest row is considered.
	expected := strings.NewReader(`# HELP akamai_property_liveness_errors_datacenter_failures Number of datacenter failures (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failures counter
akamai_property_liveness_errors_datacenter_failures{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_datacenter_failure_duration Datacenter failure duration (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failure_duration gauge
akamai_property_liveness_errors_datacenter_failure_duration{datacenter="3201",domain="example.akadns.net",property="www"} 120
`)

	require.NoError(t, testutil.CollectAndCompare(
		wrappedCollector,
		expected,
		"akamai_property_liveness_errors_datacenter_failures",
		"akamai_property_liveness_errors_datacenter_failure_duration",
	))
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the latest row's timestamp
	require.Equal(
		t,
		time.Date(2026, time.August, 13, 13, 1, 5, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)
}

func TestLivenessCollectorCollectUpdatesHistogramAndSummaryForAllNewRows(t *testing.T) {
	defer gock.Off()
	collector, registry := newTestLivenessCollector(time.Date(2026, time.August, 13, 12, 55, 0, 0, time.UTC))

	mockLivenessCollectorResponsesSingleDay(t, "2026-08-13", `{
		"metadata": {
				"date": "2026-08-13",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-13"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-13T13:01:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 60,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:01:05Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 1860,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T15:02:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 120,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	collector.Collect(make(chan prometheus.Metric, 50)) // hit Collect() so that the histogram and summary are actually populated

	expected := strings.NewReader(`# HELP akamai_property_liveness_errors_duration_per_datacenter_histogram Histogram of datacenter error duration (per domain and property)
# TYPE akamai_property_liveness_errors_duration_per_datacenter_histogram histogram
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="60"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="1800"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="3600"} 3
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="7200"} 3
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="14400"} 3
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="+Inf"} 3
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="3201",domain="example.akadns.net",property="www"} 2040
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="3201",domain="example.akadns.net",property="www"} 3
# HELP akamai_property_liveness_errors_errors_per_datacenter_summary Summary of datacenter errors (per domain and property)
# TYPE akamai_property_liveness_errors_errors_per_datacenter_summary summary
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="3201",domain="example.akadns.net",property="www"} 3
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="3201",domain="example.akadns.net",property="www"} 3
`)

	require.NoError(t, testutil.GatherAndCompare(
		registry,
		expected,
		"akamai_property_liveness_errors_duration_per_datacenter_histogram",
		"akamai_property_liveness_errors_errors_per_datacenter_summary",
	))
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the latest row's timestamp
	require.Equal(
		t,
		time.Date(2026, time.August, 13, 15, 2, 0, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)
}

func TestLivenessCollectorCollectSkipsOldRows(t *testing.T) {
	defer gock.Off()
	collector, registry := newTestLivenessCollector(time.Date(2026, time.August, 13, 12, 55, 0, 0, time.UTC))
	wrappedCollector := livenessConstMetricCollector{collector: collector}

	mockLivenessCollectorResponsesSingleDay(t, "2026-08-13", `{
		"metadata": {
				"date": "2026-08-13",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-13"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-13T10:01:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 3605,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T10:01:05Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 1860,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:01:05Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 30,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:12:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 60,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	// const metrics should report 1 for the counter and 60 for the duration since only the latest row is considered.
	expectedConst := strings.NewReader(`# HELP akamai_property_liveness_errors_datacenter_failures Number of datacenter failures (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failures counter
akamai_property_liveness_errors_datacenter_failures{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_datacenter_failure_duration Datacenter failure duration (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failure_duration gauge
akamai_property_liveness_errors_datacenter_failure_duration{datacenter="3201",domain="example.akadns.net",property="www"} 60
`)

	require.NoError(t, testutil.CollectAndCompare(
		wrappedCollector,
		expectedConst,
		"akamai_property_liveness_errors_datacenter_failures",
		"akamai_property_liveness_errors_datacenter_failure_duration",
	))

	// the first 2 rows are older, and should not be counted. only durations within 60s should be observed, as the longer failures are too old.
	expectedRegistered := strings.NewReader(`# HELP akamai_property_liveness_errors_duration_per_datacenter_histogram Histogram of datacenter error duration (per domain and property)
# TYPE akamai_property_liveness_errors_duration_per_datacenter_histogram histogram
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="60"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="1800"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="3600"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="7200"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="14400"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="+Inf"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="3201",domain="example.akadns.net",property="www"} 90
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="3201",domain="example.akadns.net",property="www"} 2
# HELP akamai_property_liveness_errors_errors_per_datacenter_summary Summary of datacenter errors (per domain and property)
# TYPE akamai_property_liveness_errors_errors_per_datacenter_summary summary
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="3201",domain="example.akadns.net",property="www"} 2
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="3201",domain="example.akadns.net",property="www"} 2
`)

	require.NoError(t, testutil.GatherAndCompare(
		registry,
		expectedRegistered,
		"akamai_property_liveness_errors_duration_per_datacenter_histogram",
		"akamai_property_liveness_errors_errors_per_datacenter_summary",
	))
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the latest row's timestamp
	require.Equal(
		t,
		time.Date(2026, time.August, 13, 13, 12, 0, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)
}

func TestLivenessCollectorCollectMultipleSeriesHistogramSummary(t *testing.T) {
	defer gock.Off()
	collector, registry := newTestLivenessCollector(time.Date(2026, time.August, 13, 12, 55, 0, 0, time.UTC))

	mockLivenessCollectorResponsesSingleDay(t, "2026-08-13", `{
		"metadata": {
				"date": "2026-08-13",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-13"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-13T13:01:00Z",
				"datacenters": [
					{
						"datacenterId": 42,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 7200,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:01:05Z",
				"datacenters": [
					{
						"datacenterId": 42,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 1860,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:01:10Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 65,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			},
			{
				"timestamp": "2026-08-13T13:15:05Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 60,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	collector.Collect(make(chan prometheus.Metric, 50)) // hit Collect() so that the histogram and summary are actually populated

	expected := strings.NewReader(`# HELP akamai_property_liveness_errors_duration_per_datacenter_histogram Histogram of datacenter error duration (per domain and property)
# TYPE akamai_property_liveness_errors_duration_per_datacenter_histogram histogram
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="60"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="1800"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="3600"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="7200"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="14400"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="+Inf"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="42",domain="example.akadns.net",property="www",le="60"} 0
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="42",domain="example.akadns.net",property="www",le="1800"} 0
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="42",domain="example.akadns.net",property="www",le="3600"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="42",domain="example.akadns.net",property="www",le="7200"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="42",domain="example.akadns.net",property="www",le="14400"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="42",domain="example.akadns.net",property="www",le="+Inf"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="3201",domain="example.akadns.net",property="www"} 125
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="3201",domain="example.akadns.net",property="www"} 2
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="42",domain="example.akadns.net",property="www"} 9060
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="42",domain="example.akadns.net",property="www"} 2
# HELP akamai_property_liveness_errors_errors_per_datacenter_summary Summary of datacenter errors (per domain and property)
# TYPE akamai_property_liveness_errors_errors_per_datacenter_summary summary
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="3201",domain="example.akadns.net",property="www"} 2
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="3201",domain="example.akadns.net",property="www"} 2
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="42",domain="example.akadns.net",property="www"} 2
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="42",domain="example.akadns.net",property="www"} 2
`)

	require.NoError(t, testutil.GatherAndCompare(
		registry,
		expected,
		"akamai_property_liveness_errors_duration_per_datacenter_histogram",
		"akamai_property_liveness_errors_errors_per_datacenter_summary",
	))
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the latest row's timestamp
	require.Equal(
		t,
		time.Date(2026, time.August, 13, 13, 15, 5, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)
}


func TestLivenessCollectorCollectAdvancesDayBoundaryFromFailure(t *testing.T) {
	defer gock.Off()
	// lastTimestamp falls within window but is days before window end, as there has not been a failure in a while
	collector, registry := newTestLivenessCollector(time.Date(2026, time.August, 11, 5, 30, 0, 0, time.UTC))
	wrappedCollector := livenessConstMetricCollector{collector: collector}

	// the first call to retrieveLivenessTraffic() should be for Aug 11 (lastTimeStamp) and will contain the already processed row
    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/window").
        Reply(200).
        JSON(map[string]string{
            "start": "2026-08-10T00:00:00Z",
            "end":   "2026-08-14T23:59:59Z",
        })

    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www").
        Reply(200).
        BodyString(`{ 
			"metadata": {
			"date": "2026-08-11",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-11"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-11T10:05:30Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 3101,
						"duration": 1297,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

    // The second call should be for the window end (Aug 14), which contains a new failure
	gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/window").
        Reply(200).
        JSON(map[string]string{
            "start": "2026-08-10T00:00:00Z",
            "end":   "2026-08-14T23:59:59Z",
        })

    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www").
        Reply(200).
        BodyString(`{ 
			"metadata": {
			"date": "2026-08-14",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-14"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-14T03:15:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 999,
						"duration": 24,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	// the const metrics should reflect the second day's single value
	expectedConst := strings.NewReader(`# HELP akamai_property_liveness_errors_datacenter_failures Number of datacenter failures (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failures counter
akamai_property_liveness_errors_datacenter_failures{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_datacenter_failure_duration Datacenter failure duration (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failure_duration gauge
akamai_property_liveness_errors_datacenter_failure_duration{datacenter="3201",domain="example.akadns.net",property="www"} 24
`)

	require.NoError(t, testutil.CollectAndCompare(
		wrappedCollector,
		expectedConst,
		"akamai_property_liveness_errors_datacenter_failures",
		"akamai_property_liveness_errors_datacenter_failure_duration",
	))

	// since the first day should not have been reprocessed, the histogram and summary should only reflect the second day's single value
	expectedRegistered := strings.NewReader(`# HELP akamai_property_liveness_errors_duration_per_datacenter_histogram Histogram of datacenter error duration (per domain and property)
# TYPE akamai_property_liveness_errors_duration_per_datacenter_histogram histogram
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="60"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="1800"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="3600"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="7200"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="14400"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="+Inf"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="3201",domain="example.akadns.net",property="www"} 24
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_errors_per_datacenter_summary Summary of datacenter errors (per domain and property)
# TYPE akamai_property_liveness_errors_errors_per_datacenter_summary summary
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="3201",domain="example.akadns.net",property="www"} 1
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="3201",domain="example.akadns.net",property="www"} 1
`)

	require.NoError(t, testutil.GatherAndCompare(
		registry,
		expectedRegistered,
		"akamai_property_liveness_errors_duration_per_datacenter_histogram",
		"akamai_property_liveness_errors_errors_per_datacenter_summary",
	))
	// both endpoints should have been called as a day boundary was crossed
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the timestamp of the single row in day 2
	require.Equal(
		t,
		time.Date(2026, time.August, 14, 3, 15, 0, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)

}


func TestLivenessCollectorCollectAdvancesDayBoundaryFromStartup(t *testing.T) {
	defer gock.Off()
	// lastTimestamp reflects startup time of the exporter (rather than a failure) and falls within window 
	// but is days before window end, as there has not been a failure since the exporter started.
	collector, registry := newTestLivenessCollector(time.Date(2026, time.August, 11, 5, 30, 0, 0, time.UTC))
	wrappedCollector := livenessConstMetricCollector{collector: collector}

	// the first call to retrieveLivenessTraffic() should ask for for Aug 11 (lastTimeStamp) and contains no failures. 
    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/window").
        Reply(200).
        JSON(map[string]string{
            "start": "2026-08-10T00:00:00Z",
            "end":   "2026-08-14T23:59:59Z",
        })

    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www").
        Reply(200).
        BodyString(`{ 
			"metadata": {
			"date": "2026-08-11",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-11"
		},
		"dataRows": []
	}`)

    // The second call should be for the window end (Aug 14), which contains a new failure
	gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/window").
        Reply(200).
        JSON(map[string]string{
            "start": "2026-08-10T00:00:00Z",
            "end":   "2026-08-14T23:59:59Z",
        })

    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www").
        Reply(200).
        BodyString(`{ 
			"metadata": {
			"date": "2026-08-14",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-14"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-14T03:15:00Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 999,
						"duration": 24,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	// the const metrics should reflect the second day's single value
	expectedConst := strings.NewReader(`# HELP akamai_property_liveness_errors_datacenter_failures Number of datacenter failures (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failures counter
akamai_property_liveness_errors_datacenter_failures{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_datacenter_failure_duration Datacenter failure duration (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failure_duration gauge
akamai_property_liveness_errors_datacenter_failure_duration{datacenter="3201",domain="example.akadns.net",property="www"} 24
`)

	require.NoError(t, testutil.CollectAndCompare(
		wrappedCollector,
		expectedConst,
		"akamai_property_liveness_errors_datacenter_failures",
		"akamai_property_liveness_errors_datacenter_failure_duration",
	))

	// the histogram and summary should reflect the second day's single value
	expectedRegistered := strings.NewReader(`# HELP akamai_property_liveness_errors_duration_per_datacenter_histogram Histogram of datacenter error duration (per domain and property)
# TYPE akamai_property_liveness_errors_duration_per_datacenter_histogram histogram
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="60"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="1800"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="3600"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="7200"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="14400"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="+Inf"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="3201",domain="example.akadns.net",property="www"} 24
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_errors_per_datacenter_summary Summary of datacenter errors (per domain and property)
# TYPE akamai_property_liveness_errors_errors_per_datacenter_summary summary
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="3201",domain="example.akadns.net",property="www"} 1
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="3201",domain="example.akadns.net",property="www"} 1
`)

	require.NoError(t, testutil.GatherAndCompare(
		registry,
		expectedRegistered,
		"akamai_property_liveness_errors_duration_per_datacenter_histogram",
		"akamai_property_liveness_errors_errors_per_datacenter_summary",
	))
	// both endpoints should have been called as a day boundary was crossed
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the timestamp of the single row in day 2
	require.Equal(
		t,
		time.Date(2026, time.August, 14, 3, 15, 0, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)

}


func TestLivenessCollectorCollectAdvancesDayBoundaryOutsideWindow(t *testing.T) {
	defer gock.Off()
	// lastTimestamp is so far behind that the date it represents is outside the window.
	// the collector should advance to the window end and request that date.
	collector, registry := newTestLivenessCollector(time.Date(2026, time.August, 8, 10, 25, 0, 0, time.UTC))
	wrappedCollector := livenessConstMetricCollector{collector: collector}

	// the first and only call to retrieveLivenessTraffic() should ask for Aug 14 (end of the window) as lastTimestamp was from a day too old to request.
    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/window").
        Reply(200).
        JSON(map[string]string{
            "start": "2026-08-10T00:00:00Z",
            "end":   "2026-08-14T23:59:59Z",
        })

    gock.New(mockAkamaiBaseURL).
        Get("/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www").
        Reply(200).
        BodyString(`{ 
			"metadata": {
			"date": "2026-08-14",
			"domain": "example.akadns.net",
			"property": "www",
				"uri": "https://example.invalid/gtm-api/v1/reports/liveness-tests/domains/example.akadns.net/properties/www?date=2026-08-14"
		},
		"dataRows": [
			{
				"timestamp": "2026-08-14T15:25:05Z",
				"datacenters": [
					{
						"datacenterId": 3201,
						"agentIp": "204.1.136.239",
						"testName": "Our defences",
						"errorCode": 999,
						"duration": 48,
						"nickname": "Winterfell",
						"trafficTargetName": "Winterfell - 1.2.3.4",
						"targetIp": "1.2.3.4"
					}
				]
			}
		]
	}`)

	expectedConst := strings.NewReader(`# HELP akamai_property_liveness_errors_datacenter_failures Number of datacenter failures (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failures counter
akamai_property_liveness_errors_datacenter_failures{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_datacenter_failure_duration Datacenter failure duration (per domain, property, datacenter)
# TYPE akamai_property_liveness_errors_datacenter_failure_duration gauge
akamai_property_liveness_errors_datacenter_failure_duration{datacenter="3201",domain="example.akadns.net",property="www"} 48
`)

	require.NoError(t, testutil.CollectAndCompare(
		wrappedCollector,
		expectedConst,
		"akamai_property_liveness_errors_datacenter_failures",
		"akamai_property_liveness_errors_datacenter_failure_duration",
	))

	expectedRegistered := strings.NewReader(`# HELP akamai_property_liveness_errors_duration_per_datacenter_histogram Histogram of datacenter error duration (per domain and property)
# TYPE akamai_property_liveness_errors_duration_per_datacenter_histogram histogram
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="60"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="1800"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="3600"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="7200"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="14400"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_bucket{datacenter="3201",domain="example.akadns.net",property="www",le="+Inf"} 1
akamai_property_liveness_errors_duration_per_datacenter_histogram_sum{datacenter="3201",domain="example.akadns.net",property="www"} 48
akamai_property_liveness_errors_duration_per_datacenter_histogram_count{datacenter="3201",domain="example.akadns.net",property="www"} 1
# HELP akamai_property_liveness_errors_errors_per_datacenter_summary Summary of datacenter errors (per domain and property)
# TYPE akamai_property_liveness_errors_errors_per_datacenter_summary summary
akamai_property_liveness_errors_errors_per_datacenter_summary_sum{datacenter="3201",domain="example.akadns.net",property="www"} 1
akamai_property_liveness_errors_errors_per_datacenter_summary_count{datacenter="3201",domain="example.akadns.net",property="www"} 1
`)

	require.NoError(t, testutil.GatherAndCompare(
		registry,
		expectedRegistered,
		"akamai_property_liveness_errors_duration_per_datacenter_histogram",
		"akamai_property_liveness_errors_errors_per_datacenter_summary",
	))
	require.True(t, gock.IsDone(), "expected mocked liveness endpoints to be called")
	// LastTimestamp should be updated to the timestamp of the single row
	require.Equal(
		t,
		time.Date(2026, time.August, 14, 15, 25, 5, 0, time.UTC),
		collector.LastTimestamp["example.akadns.net"]["www"],
	)

}
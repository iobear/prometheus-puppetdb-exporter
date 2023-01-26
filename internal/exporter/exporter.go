package exporter

import (
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"

	"github.com/EncoreTechnologies/prometheus-puppetdb-exporter/internal/puppetdb"
)

// Exporter implements the prometheus.Exporter interface, and exports PuppetDB metrics
type Exporter struct {
	client    *puppetdb.PuppetDB
	namespace string
	metrics   map[string]*prometheus.GaugeVec
}

type metric struct {
	labels prometheus.Labels
	value  float64
}

var (
	metricMap = map[string]string{
		"node_status_count": "node_status_count",
	}
)

// NewPuppetDBExporter returns a new exporter of PuppetDB metrics.
func NewPuppetDBExporter(url, certPath, caPath, keyPath string, sslSkipVerify bool, categories map[string]struct{}) (e *Exporter, err error) {
	e = &Exporter{
		namespace: "puppetdb",
	}

	opts := &puppetdb.Options{
		URL:        url,
		CertPath:   certPath,
		CACertPath: caPath,
		KeyPath:    keyPath,
		SSLVerify:  sslSkipVerify,
	}

	e.client, err = puppetdb.NewClient(opts)
	if err != nil {
		log.Fatalf("failed to create new client: %s", err)
		return
	}

	e.initGauges(categories)

	return
}

// Describe outputs PuppetDB metric descriptions
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range e.metrics {
		m.Describe(ch)
	}
}

// Collect fetches new metrics from the PuppetDB and updates the appropriate metrics
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	for _, m := range e.metrics {
		m.Collect(ch)
	}
}

// Scrape scrapes PuppetDB and update metrics
func (e *Exporter) Scrape(interval time.Duration, unreportedNode string, verbose bool, categories map[string]struct{}) {
	var statuses map[string]int

	unreportedDuration, err := time.ParseDuration(unreportedNode)
	if err != nil {
		log.Errorf("failed to parse unreported duration: %s", err)
		return
	}

	const unreportedStr = "unreported"

	for {
		statusStr := ""
		statuses = make(map[string]int)

		nodes, err := e.client.Nodes()
		if err != nil {
			log.Errorf("failed to get nodes: %s", err)
		}

		reports := map[string][]metric{}

		for _, node := range nodes {
			var deactivated, reasonStr string
			var unreported bool

			debugStr := "Node: %s / Unreported Reason: %s\n"

			// This doesn't matter too much for unreported status
			if node.Deactivated == "" {
				deactivated = "false"
			} else {
				deactivated = "true"
			}

			// Note: The unreported nodes in puppetboard (front end) will filter out nodes in
			// the puppetdb if they have gone unreported for a long time (~1 week+). These nodes
			// are queryable via the API and will not have a "lastestReport" on them.
			// These nodes are NOT listed in puppetboard under "unreported" nodes either.
			if node.ReportTimestamp == "" {
				if !unreported {
					reasonStr = "Timestamp string is blank"

					if verbose {
						log.Debugf(debugStr, node.Certname, reasonStr)
					}
				}

				statusStr = unreportedStr
				unreported = true
			}
			latestReport, err := time.Parse("2006-01-02T15:04:05Z", node.ReportTimestamp)
			if err != nil {
				if !unreported {
					reasonStr = "Invalid time parsed"

					if verbose {
						log.Debugf(debugStr, node.Certname, reasonStr)
					}
				}

				statusStr = unreportedStr
				unreported = true
			}

			if latestReport.Add(unreportedDuration).Before(time.Now()) {
				if !unreported {
					reasonStr = fmt.Sprintf("Latest timestamp older than %s", unreportedDuration)

					if verbose {
						log.Debugf(debugStr, node.Certname, reasonStr)
					}
				}

				unreported = true
				statusStr = unreportedStr
			} else if node.LatestReportStatus == "" {
				if !unreported {
					reasonStr = "Unreported status"

					if verbose {
						log.Debugf(debugStr, node.Certname, reasonStr)
					}
				}

				unreported = true
				statusStr = unreportedStr
			} else {
				statuses[node.LatestReportStatus]++
				statusStr = node.LatestReportStatus
			}

			if unreported {
				statuses["unreported"]++
			}

			reports["report"] = append(reports["report"], metric{
				labels: prometheus.Labels{
					"environment": node.ReportEnvironment,
					"host":        node.Certname,
					"deactivated": deactivated,
					"status":      statusStr,
					"reason":      reasonStr,
				},
				value: float64(latestReport.Unix()),
			})

			if node.LatestReportHash != "" {
				reportMetrics, _ := e.client.ReportMetrics(node.LatestReportHash)
				for _, reportMetric := range reportMetrics {
					_, ok := categories[reportMetric.Category]
					if ok {
						category := fmt.Sprintf("report_%s", reportMetric.Category)
						reports[category] = append(reports[category], metric{
							labels: prometheus.Labels{
								"name":        strings.ReplaceAll(strings.Title(reportMetric.Name), "_", " "),
								"environment": node.ReportEnvironment,
								"deactivated": deactivated,
								"host":        node.Certname,
								"status":      statusStr,
								"reason":      reasonStr,
							},
							value: reportMetric.Value,
						})
					}
				}
			}
		}

		e.metrics["node_report_status_count"].Reset()

		for statusName, statusValue := range statuses {
			e.metrics["node_report_status_count"].With(prometheus.Labels{"status": statusName}).Set(float64(statusValue))
		}

		for k, m := range e.metrics {
			if k != "node_report_status_count" {
				m.Reset()

				for _, t := range reports[k] {
					m.With(t.labels).Set(t.value)
				}
			}
		}

		reports = nil

		time.Sleep(interval)
	}
}

func (e *Exporter) initGauges(categories map[string]struct{}) {
	e.metrics = map[string]*prometheus.GaugeVec{}

	e.metrics["node_report_status_count"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: e.namespace,
		Name:      "node_report_status_count",
		Help:      "Total count of reports status by type",
	}, []string{"status"})

	for category := range categories {
		metricName := fmt.Sprintf("report_%s", category)
		e.metrics[metricName] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "puppet",
			Name:      metricName,
			Help:      fmt.Sprintf("Total count of %s per status", category),
		}, []string{"name", "environment", "host", "deactivated", "status", "reason"})

	}

	e.metrics["report"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "puppet",
		Name:      "report",
		Help:      "Timestamp of latest report",
	}, []string{"environment", "host", "deactivated", "status", "reason"})

	for _, m := range e.metrics {
		prometheus.MustRegister(m)
	}
}

package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	ping "github.com/digineo/go-ping"
	mon "github.com/digineo/go-ping/monitor"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"

	"github.com/spf13/pflag"
)

const version = "0.5.1"

var (
	showHelp    = pflag.BoolP("help", "h", false, "Show usage")
	showVersion = pflag.BoolP("version", "v", false, "Print version information")
	configName  = pflag.StringP("config", "c", "/etc/ping-exporter/ping-exporter.yaml", "Config file to use")
)

func printVersion() {
	fmt.Println("ping-exporter")
	fmt.Printf("Version: %s\n", version)
	fmt.Println("Author(s): Tobias Famulla")
}

func startMonitor(config PingConfig, dnsRefresh time.Duration) (*mon.Monitor, error) {
	pinger, err := ping.New(config.SourceV4, config.SourceV6)
	if err != nil {
		return nil, err
	}

	monitor := mon.New(pinger, config.PingInterval, config.PingTimeout)
	targets := []*pingTarget(nil)

	for i, host := range config.PingTargets {
		t := &pingTarget{
			host:      host,
			addresses: make([]net.IP, 0),
			delay:     time.Duration(10*i) * time.Millisecond,
			sourceV4:  config.SourceV4,
			sourceV6:  config.SourceV6,
		}
		targets = append(targets, t)

		err := t.addOrUpdateMonitor(monitor)
		if err != nil {
			log.Errorln(err)
		}
	}

	go startDNSAutoRefresh(dnsRefresh, targets, monitor)

	return monitor, nil

}

func startDNSAutoRefresh(dnsRefresh time.Duration, targets []*pingTarget, monitor *mon.Monitor) {
	if dnsRefresh == 0 {
		return
	}

	for {
		select {
		case <-time.After(dnsRefresh):
			refreshDNS(targets, monitor)
		}
	}
}

func refreshDNS(targets []*pingTarget, monitor *mon.Monitor) {
	for _, t := range targets {
		log.Infof("refreshing DNS")

		go func(ta *pingTarget) {
			err := ta.addOrUpdateMonitor(monitor)
			if err != nil {
				log.Errorf("could refresh dns: %v", err)
			}
		}(t)
	}
}

func startServer(monitor []*mon.Monitor, metricsPath string, listenAddress string) {

	log.Infof("starting ping-exporter (Version: %s)", version)

	infoPage := []byte(`<!doctype html>
	<html>
		<head><title>PING Exporter (Version ` + version + `)</title></head>
		<body>
		<h1>PING Exporter</h1>
		<p><a href="` + metricsPath + `">Metrics</a></p>
		</body>
	</html>`)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(infoPage)
	})

	registry := prometheus.NewRegistry()
	registry.MustRegister(&pingCollector{monitors: monitor})

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorLog:      log.NewErrorLogger(),
		ErrorHandling: promhttp.ContinueOnError})
	http.HandleFunc(metricsPath, h.ServeHTTP)
	log.Infof("Listening for %s on %s", metricsPath, listenAddress)
	log.Fatal(http.ListenAndServe(listenAddress, nil))
}

func main() {

	pflag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		pflag.PrintDefaults()
	}
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	if *showHelp {
		pflag.Usage()
		os.Exit(0)
	}
	if *showVersion {
		printVersion()
		os.Exit(0)
	}

	var flags *pflag.FlagSet
	flags = pflag.CommandLine

	if flags.Changed("config") {
		if _, err := os.Stat(*configName); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Configuration file does not exist:%s\n", *configName)
			os.Exit(4)
		}
	}

	config, err := newConfiguration(flags)
	if err != nil {
		log.Errorln(err)
		os.Exit(3)
	}

	if !config.hasPingMultiConfig {
		config.pingConfigurations = []PingConfig{
			PingConfig{config.pingSourceV4, config.pingSourceV6, config.pingTarget, config.pingInterval, config.pingTimeout},
		}
	}

	var monitors []*mon.Monitor
	for _, c := range config.pingConfigurations {
		m, err := startMonitor(c, config.dnsRefresh)
		if err != nil {
			log.Errorln(err)
			os.Exit(2)
		}
		monitors = append(monitors, m)
	}

	startServer(monitors, config.metricsPath, config.listenAddress)
}

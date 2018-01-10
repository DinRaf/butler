package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"

	"git.corp.adobe.com/TechOps-IAO/butler/config"
	"git.corp.adobe.com/TechOps-IAO/butler/environment"

	"github.com/jasonlvhit/gocron"
	log "github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	version                 = "v1.1.0"
	PrometheusConfig        = "prometheus.yml"
	PrometheusConfigStatic  = "prometheus.yml"
	AdditionalConfig        = "alerts/commonalerts.yml,alerts/tenant.yml"
	PrometheusRootDirectory = "/opt/prometheus"
	PrometheusHost          string
	ButlerConfigInterval    = 300
	ButlerConfigUrl         string
	ConfigCache             map[string][]byte
	AllConfigFiles          []string
	PrometheusConfigFiles   []string
	AdditionalConfigFiles   []string
	MustacheSubs            map[string]string
	HttpTimeout             = 10
	HttpRetries             = 4
	HttpRetryWaitMin        = 5
	HttpRetryWaitMax        = 10
)

// Monitor is the empty structure to be used for starting up the monitor
// health check and prometheus metrics http endpoints.
type Monitor struct {
	Config *config.ButlerConfig
}

// NewMonitor returns a Monitor structure which is used to bring up the
// monitor health check and prometheus metrics http endpoints.
func NewMonitor(bc *config.ButlerConfig) *Monitor {
	return &Monitor{Config: bc}
}

// MonitorOutput is the structure which holds the formatting which is output
// to the health check monitor. When /health-check is hit, it returns this
// structure, which is then Marshal'd to json and provided back to the end
// user
type MonitorOutput struct {
	ConfigPath       string                `json:"config-path"`
	ConfigScheme     string                `json:"config-scheme"`
	RetrieveInterval int                   `json:"retrieve-interval"`
	LogLevel         log.Level             `json:"log-level"`
	ConfigSettings   config.ConfigSettings `json:"config-settings"`
	Version          string                `json:"version"`
}

// Start turns up the http server for monitoring butler.
func (m *Monitor) Start() {
	http.HandleFunc("/health-check", m.MonitorHandler)
	http.Handle("/metrics", promhttp.Handler())
	server := &http.Server{}
	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("Error creating listener: %s", err.Error())
	}
	go server.Serve(listener)
}

// MonitorHandler is the handler function for the /health-check monitor
// endpoint. It displays the JSON Marshal'd output of all the various
// configuration options that buter gets started with, and some run time
// information
func (m *Monitor) MonitorHandler(w http.ResponseWriter, r *http.Request) {
	mOut := MonitorOutput{ConfigPath: m.Config.GetPath(),
		ConfigScheme:     m.Config.Scheme,
		RetrieveInterval: m.Config.Interval,
		LogLevel:         m.Config.GetLogLevel(),
		ConfigSettings:   *m.Config.Config,
		Version:          version}
	resp, err := json.Marshal(mOut)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "Could not Marshal JSON, but I promise I'm up!")
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, string(resp))
}

func SetLogLevel(l string) log.Level {
	switch strings.ToLower(l) {
	case "debug":
		return log.DebugLevel
	case "info":
		return log.InfoLevel
	case "warn":
		return log.WarnLevel
	case "error":
		return log.ErrorLevel
	case "fatal":
		return log.FatalLevel
	case "panic":
		return log.PanicLevel
	default:
		log.Warn(fmt.Sprintf("Unknown log level \"%s\". Defaulting to %s", l, log.InfoLevel))
		return log.InfoLevel
	}
}

func main() {
	var (
		err                    error
		versionFlag            = flag.Bool("version", false, "Print version information.")
		configPath             = flag.String("config.path", "", "Full remote path to butler configuration file (eg: full URL scheme://path).")
		configInterval         = flag.String("config.retrieve-interval", fmt.Sprintf("%v", ButlerConfigInterval), "The interval, in seconds, to retrieve new butler configuration files.")
		configHttpTimeout      = flag.String("http.timeout", fmt.Sprintf("%v", HttpTimeout), "The http timeout, in seconds, for GET requests to obtain the butler configuration file.")
		configHttpRetries      = flag.String("http.retries", fmt.Sprintf("%v", HttpRetries), "The number of http retries for GET requests to obtain the butler configuration files")
		configHttpRetryWaitMin = flag.String("http.retry_wait_min", fmt.Sprintf("%v", HttpRetryWaitMin), "The minimum amount of time to wait before attemping to retry the http config get operation.")
		configHttpRetryWaitMax = flag.String("http.retry_wait_max", fmt.Sprintf("%v", HttpRetryWaitMax), "The maximum amount of time to wait before attemping to retry the http config get operation.")
		configS3Region         = flag.String("s3.region", "", "The S3 Region that the config file resides.")
		configLogLevel         = flag.String("log.level", "info", "The butler log level. Log levels are: debug, info, warn, error, fatal, panic.")
	)
	flag.Parse()
	newConfigLogLevel := environment.GetVar(*configLogLevel)
	log.SetLevel(SetLogLevel(newConfigLogLevel))
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true})

	if *versionFlag {
		fmt.Fprintf(os.Stdout, "butler %s\n", version)
		os.Exit(0)
	}

	if *configPath == "" {
		log.Fatal("You must provide a -config.path for a path to the butler configuration.")
	}

	log.Infof("Starting butler version %s", version)

	newConfigPath := environment.GetVar(*configPath)
	pathSplit := strings.Split(newConfigPath, "://")
	if len(pathSplit) != 2 {
		log.Fatalf("Cannot properly parse -config.path. -config.path must be in URL form. -config.path=%v", newConfigPath)
	}
	scheme := strings.ToLower(pathSplit[0])
	path := pathSplit[1]

	bc := config.NewButlerConfig()
	bc.SetLogLevel(SetLogLevel(newConfigLogLevel))
	bc.SetScheme(scheme)
	bc.SetPath(path)

	log.Debugf("main(): scheme=%#v path=%#v bc=%#v", scheme, path, bc)

	switch scheme {
	case "http", "https":
		// Set the HTTP Timeout
		newConfigHttpTimeout, _ := strconv.Atoi(environment.GetVar(*configHttpTimeout))
		if newConfigHttpTimeout == 0 {
			newConfigHttpTimeout = HttpTimeout
		}
		log.Debugf("main(): setting HttpTimeout to %d", newConfigHttpTimeout)
		bc.SetTimeout(newConfigHttpTimeout)

		// Set the HTTP Retries Counter
		newConfigHttpRetries, _ := strconv.Atoi(environment.GetVar(*configHttpRetries))
		if newConfigHttpRetries == 0 {
			newConfigHttpRetries = HttpRetries
		}
		log.Debugf("main(): setting HttpRetries to %d", newConfigHttpRetries)
		bc.SetRetries(newConfigHttpRetries)

		// Set the HTTP Holdoff Values
		newConfigHttpRetryWaitMin, _ := strconv.Atoi(environment.GetVar(*configHttpRetryWaitMin))
		if newConfigHttpRetryWaitMin == 0 {
			newConfigHttpRetryWaitMin = HttpRetryWaitMin
		}
		newConfigHttpRetryWaitMax, _ := strconv.Atoi(environment.GetVar(*configHttpRetryWaitMax))
		if newConfigHttpRetryWaitMax == 0 {
			newConfigHttpRetryWaitMax = HttpRetryWaitMax
		}
		log.Debugf("main(): setting RetryWaitMin[%d] and RetryWaitMax[%d]", newConfigHttpRetryWaitMin, newConfigHttpRetryWaitMax)
		bc.SetRetryWaitMin(newConfigHttpRetryWaitMin)
		bc.SetRetryWaitMax(newConfigHttpRetryWaitMax)
	case "s3", "S3":
		if *configS3Region == "" {
			log.Fatalf("You must provide a -s3.region for use with the s3 downloader.")
		}
		newConfigS3Region := environment.GetVar(*configS3Region)
		log.Debugf("main(): setting s3 region=%v", newConfigS3Region)
		bc.SetRegion(newConfigS3Region)
	case "file":
		newPath := environment.GetVar(path)
		log.Debugf("main(): setting file path=%v", newPath)
		// stegen gotta figure out what is up here!
		bc.SetPath(newPath)
	}

	// Set the butler configuration retrieval interval
	newConfigInterval, _ := strconv.Atoi(environment.GetVar(*configInterval))
	if newConfigInterval == 0 {
		newConfigInterval = ButlerConfigInterval
	}
	log.Debugf("main(): setting ConfigInterval to %d", newConfigInterval)

	bc.SetInterval(newConfigInterval)

	if err = bc.Init(); err != nil {
		log.Fatalf("Cannot initialize butler config. err=%s", err.Error())
	}

	// Start up the monitor web server
	monitor := NewMonitor(bc)
	monitor.Start()

	// Do initial grab of butler configuration file.
	// Going to do this in an endless loop until we initially
	// grab a configuration file.
	for {
		log.Debugf("main(): running first bc.Handler()")
		err = bc.Handler()

		if err != nil {
			log.Infof("Cannot retrieve butler configuration. err=%s", err.Error())
			log.Infof("Sleeping 5 seconds.")
			time.Sleep(5 * time.Second)
		} else {
			break
		}
	}

	sched := gocron.NewScheduler()
	log.Debugf("main(): starting scheduler...")

	log.Debugf("main(): running butler configuration scheduler every %d seconds", bc.GetInterval())
	sched.Every(uint64(bc.GetInterval())).Seconds().Do(bc.Handler)

	log.Debugf("main(): giving scheduler to butler.")
	bc.SetScheduler(sched)

	log.Debugf("main(): doing initial run of butler configuration management handler")
	bc.RunCMHandler()

	<-sched.Start()
}

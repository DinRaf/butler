/*
Copyright 2017 Adobe. All rights reserved.
This file is licensed to you under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License. You may obtain a copy
of the License at http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under
the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR REPRESENTATIONS
OF ANY KIND, either express or implied. See the License for the specific language
governing permissions and limitations under the License.
*/

package main

import (
	"flag"
	"fmt"
	_ "net/http/pprof"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adobe/butler/internal/config"
	"github.com/adobe/butler/internal/environment"
	"github.com/adobe/butler/internal/monitor"

	"github.com/jasonlvhit/gocron"
	log "github.com/sirupsen/logrus"
)

const (
	defaultButlerConfigInterval = 300
	defaultHTTPRetryWaitMin     = 5
	defaultHTTPRetryWaitMax     = 15
	defaultHTTPRetries          = 5
	defaultHTTPTimeout          = 10
)

var (
	version        string
	ConfigCache    map[string][]byte
	AllConfigFiles []string
	MustacheSubs   map[string]string
	butlerTesting  = false
)

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
		configInterval         = flag.String("config.retrieve-interval", fmt.Sprintf("%v", defaultButlerConfigInterval), "The interval, in seconds, to retrieve new butler configuration files.")
		configHTTPTimeout      = flag.String("http.timeout", fmt.Sprintf("%v", defaultHTTPTimeout), "The http timeout, in seconds, for GET requests to obtain the butler configuration file.")
		configHTTPRetries      = flag.String("http.retries", fmt.Sprintf("%v", defaultHTTPRetries), "The number of http retries for GET requests to obtain the butler configuration files")
		configHTTPRetryWaitMin = flag.String("http.retry_wait_min", fmt.Sprintf("%v", defaultHTTPRetryWaitMin), "The minimum amount of time to wait before attemping to retry the http config get operation.")
		configHTTPRetryWaitMax = flag.String("http.retry_wait_max", fmt.Sprintf("%v", defaultHTTPRetryWaitMax), "The maximum amount of time to wait before attemping to retry the http config get operation.")
		configHTTPAuthToken    = flag.String("http.auth_token", "", "HTTP auth token to use for HTTP authentication.")
		configHTTPAuthType     = flag.String("http.auth_type", "", "HTTP auth type (eg: basic / digest / token-key) to use. If empty (by default) do not use HTTP authentication.")
		configHTTPAuthUser     = flag.String("http.auth_user", "", "HTTP auth user to use for HTTP authentication")
		configS3Region         = flag.String("s3.region", "", "The S3 Region that the config file resides.")
		configEtcdEndpoints    = flag.String("etcd.endpoints", "", "The endpoints to connect to etcd.")
		configLogLevel         = flag.String("log.level", "info", "The butler log level. Log levels are: debug, info, warn, error, fatal, panic.")
		butlerTest             = flag.Bool("test", false, "Are we testing butler? (probably not!)")
	)
	flag.Parse()
	newConfigLogLevel := environment.GetVar(*configLogLevel)
	log.SetLevel(SetLogLevel(newConfigLogLevel))
	log.SetFormatter(&log.TextFormatter{FullTimestamp: true})

	if *versionFlag {
		fmt.Fprintf(os.Stdout, "butler %s\n", version)
		os.Exit(0)
	}

	// If butlerTesting is true, then we're going to behave a little differently. We're going to treat butler as a one shot to test
	// some main butler functionality
	if *butlerTest {
		log.Warnf("Butler testing mode enabled (eg: oneshot mode).")
		butlerTesting = true
	}

	if *configPath == "" {
		log.Fatal("You must provide a -config.path for a path to the butler configuration.")
	}

	log.Infof("Starting Butler CMS version %s", version)

	newURL, err := url.Parse(environment.GetVar(*configPath))
	if err != nil || newURL.Scheme == "" {
		log.Fatalf("Cannot properly parse -config.path. -config.path must be in URL form. -config.path=%v", environment.GetVar(*configPath))
	}

	bc := config.NewButlerConfig()
	bc.URL = newURL
	bc.SetLogLevel(SetLogLevel(newConfigLogLevel))
	err = bc.SetScheme(bc.URL.Scheme)
	if err != nil {
		log.Fatalf("Unsupported butler scheme. scheme=%v", bc.URL.Scheme)
	}
	bc.SetPath(bc.URL.Path)

	switch bc.URL.Scheme {
	case "http", "https":
		newConfigHTTPAuthType := strings.ToLower(environment.GetVar(*configHTTPAuthType))
		if newConfigHTTPAuthType != "" {
			if environment.GetVar(*configHTTPAuthUser) != "" && environment.GetVar(*configHTTPAuthToken) != "" {
			} else {
				log.Fatalf("HTTP Authentication enabled, but insufficient authentication details provided.")
			}
			switch newConfigHTTPAuthType {
			case "basic", "digest", "token-key":
				bc.SetHTTPAuthType(newConfigHTTPAuthType)
				bc.SetHTTPAuthToken(*configHTTPAuthToken)
				bc.SetHTTPAuthUser(*configHTTPAuthUser)
				break
			default:
				log.Fatalf("Unsupported HTTP Authentication Type: %s", newConfigHTTPAuthType)
			}
		}
		// Set the HTTP Timeout
		newConfigHTTPTimeout, _ := strconv.Atoi(environment.GetVar(*configHTTPTimeout))
		if newConfigHTTPTimeout == 0 {
			newConfigHTTPTimeout = defaultHTTPTimeout
		}
		log.Debugf("main(): setting HttpTimeout to %d", newConfigHTTPTimeout)
		bc.SetTimeout(newConfigHTTPTimeout)

		// Set the HTTP Retries Counter
		newConfigHTTPRetries, _ := strconv.Atoi(environment.GetVar(*configHTTPRetries))
		if newConfigHTTPRetries == 0 {
			newConfigHTTPRetries = defaultHTTPRetries
		}
		log.Debugf("main(): setting HttpRetries to %d", newConfigHTTPRetries)
		bc.SetRetries(newConfigHTTPRetries)

		// Set the HTTP Holdoff Values
		newConfigHTTPRetryWaitMin, _ := strconv.Atoi(environment.GetVar(*configHTTPRetryWaitMin))
		if newConfigHTTPRetryWaitMin == 0 {
			newConfigHTTPRetryWaitMin = defaultHTTPRetryWaitMin
		}
		newConfigHTTPRetryWaitMax, _ := strconv.Atoi(environment.GetVar(*configHTTPRetryWaitMax))
		if newConfigHTTPRetryWaitMax == 0 {
			newConfigHTTPRetryWaitMax = defaultHTTPRetryWaitMax
		}
		log.Debugf("main(): setting RetryWaitMin[%d] and RetryWaitMax[%d]", newConfigHTTPRetryWaitMin, newConfigHTTPRetryWaitMax)
		bc.SetRetryWaitMin(newConfigHTTPRetryWaitMin)
		bc.SetRetryWaitMax(newConfigHTTPRetryWaitMax)
	case "s3", "S3":
		if *configS3Region == "" {
			log.Fatalf("You must provide a -s3.region for use with the s3 downloader.")
		}
		newConfigS3Region := environment.GetVar(*configS3Region)
		log.Debugf("main(): setting s3 region=%v", newConfigS3Region)
		bc.SetRegion(newConfigS3Region)
	case "blob":
		os.Setenv("BUTLER_STORAGE_ACCOUNT", bc.URL.Host)
	case "etcd":
		if *configEtcdEndpoints == "" {
			log.Fatalf("You must provide a valid -etcd.endpoints for use with the etcd downloader.")
		}
		newConfigEtcdEndpoints := environment.GetVar(*configEtcdEndpoints)
		log.Debugf("main(): setting etcd endpoints=%v", newConfigEtcdEndpoints)
		bc.SetEndpoints(strings.Split(newConfigEtcdEndpoints, ","))
	}

	// Set the butler configuration retrieval interval
	newConfigInterval, _ := strconv.Atoi(environment.GetVar(*configInterval))
	if newConfigInterval == 0 {
		newConfigInterval = defaultButlerConfigInterval
	}
	log.Debugf("main(): setting ConfigInterval to %d", newConfigInterval)

	bc.SetInterval(newConfigInterval)

	if err = bc.Init(); err != nil {
		log.Fatalf("Cannot initialize butler config. err=%s", err.Error())
	}

	// Do initial grab of butler configuration file.
	// Going to do this in an endless loop until we initially
	// grab a configuration file.
	for {
		log.Infof("main(): Loading initial butler configuration.")
		log.Debugf("main(): running first bc.Handler()")
		err = bc.Handler()

		if err != nil {
			if butlerTesting {
				log.Fatalf("Cannot retrieve butler configuration. err=%s butlerTesting=%#v", err.Error(), butlerTesting)
			}
			//log.Error("Cannot retrieve butler configuration. err=%s", err.Error())
			log.Warnf("main(): Sleeping 5 seconds.")
			time.Sleep(5 * time.Second)
		} else {
			log.Infof("main(): Loaded initial butler configuration.")
			break
		}
	}

	// Start up the monitor web server after we grab the monitor config values
	monitor := monitor.NewMonitor().WithOpts(&monitor.Opts{Config: bc, Version: version})
	monitor.Start()

	sched := gocron.NewScheduler()
	log.Debugf("main(): starting scheduler...")

	log.Debugf("main(): running butler configuration scheduler every %d seconds", bc.GetInterval())
	sched.Every(uint64(bc.GetInterval())).Seconds().Do(bc.Handler)

	log.Debugf("main(): giving scheduler to butler.")
	bc.SetScheduler(sched)

	log.Debugf("main(): doing initial run of butler configuration management handler")
	bc.RunCMHandler()

	if butlerTesting {
		os.Exit(0)
	} else {
		<-sched.Start()
	}
}

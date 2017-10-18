// Copyright 2014-2016 Fraunhofer Institute for Applied Information Technology FIT

package main

import (
	"flag"
	"fmt"
	"mime"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"time"

	_ "code.linksmart.eu/com/go-sec/auth/keycloak/validator"
	"code.linksmart.eu/com/go-sec/auth/validator"
	"code.linksmart.eu/sc/service-catalog/catalog"
	"github.com/codegangsta/negroni"
	"github.com/gorilla/context"
	"github.com/justinas/alice"
	"github.com/oleksandr/bonjour"
)

var (
	confPath = flag.String("conf", "conf/service-catalog.json", "Service catalog configuration file path")
	profile  = flag.Bool("profile", false, "Enable the HTTP server for runtime profiling")
	version  = flag.Bool("version", false, "Show the Service Catalog API version")
)

const LINKSMART = `
╦   ╦ ╔╗╔ ╦╔═  ╔═╗ ╔╦╗ ╔═╗ ╦═╗ ╔╦╗ R
║   ║ ║║║ ╠╩╗  ╚═╗ ║║║ ╠═╣ ╠╦╝  ║
╩═╝ ╩ ╝╚╝ ╩ ╩  ╚═╝ ╩ ╩ ╩ ╩ ╩╚═  ╩
`

var Version = "MAJOR.MINOR.PATCH Not Provided"

func main() {
	flag.Parse()
	if *version {
		fmt.Println(Version)
		return
	}
	fmt.Print(LINKSMART)
	logger.Printf("Starting Service Catalog - Version %s", Version)

	if *profile {
		logger.Println("Starting runtime profiling server")
		go func() { logger.Println(http.ListenAndServe("0.0.0.0:6060", nil)) }()
	}

	config, err := loadConfig(*confPath)
	if err != nil {
		logger.Fatalf("Error reading config file %v: %v", *confPath, err)
	}

	r, shutdownAPI, err := setupRouter(config)
	if err != nil {
		logger.Fatal(err.Error())
	}

	// Announce service using DNS-SD
	var bonjourS *bonjour.Server
	if config.DnssdEnabled {
		bonjourS, err = bonjour.Register(config.Description,
			catalog.DNSSDServiceType,
			"",
			config.BindPort,
			[]string{"uri=/"},
			nil)
		if err != nil {
			logger.Printf("Failed to register DNS-SD service: %s", err.Error())
		} else {
			logger.Println("Registered service via DNS-SD using type", catalog.DNSSDServiceType)
		}
	}

	// Ctrl+C / Kill handling
	handler := make(chan os.Signal, 1)
	signal.Notify(handler, os.Interrupt, os.Kill)
	go func() {
		<-handler
		// Place last will logic here

		// Stop bonjour registration
		if bonjourS != nil {
			bonjourS.Shutdown()
			time.Sleep(1e9)
		}

		// Shutdown catalog API
		err := shutdownAPI()
		if err != nil {
			logger.Println(err.Error())
		}

		logger.Println("Stopped")
		os.Exit(0)
	}()

	err = mime.AddExtensionType(".jsonld", "application/ld+json")
	if err != nil {
		logger.Println("ERROR: ", err.Error())
	}

	// Configure the middleware
	n := negroni.New(
		negroni.NewRecovery(),
		logger,
	)
	// Mount router
	n.UseHandler(r)

	// Start listener
	endpoint := fmt.Sprintf("%s:%s", config.BindAddr, strconv.Itoa(config.BindPort))
	//logger.Printf("Listening on %v%v", endpoint, config.ApiLocation)
	n.Run(endpoint)
}

func setupRouter(config *Config) (*router, func() error, error) {
	var listeners []catalog.Listener

	// Setup API storage
	var (
		storage catalog.Storage
		err     error
	)
	switch config.Storage.Type {
	case catalog.CatalogBackendMemory:
		storage = catalog.NewMemoryStorage()
	case catalog.CatalogBackendLevelDB:
		storage, err = catalog.NewLevelDBStorage(config.Storage.DSN, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("Failed to start LevelDB storage: %v", err.Error())
		}
	default:
		return nil, nil, fmt.Errorf("Could not create catalog API storage. Unsupported type: %v", config.Storage.Type)
	}

	controller, err := catalog.NewController(storage, listeners...)
	if err != nil {
		storage.Close()
		return nil, nil, fmt.Errorf("Failed to start the controller: %v", err.Error())
	}

	//create mqtt api
	catalog.NewMQTTAPI(controller, config.MQTTConf)

	// Create catalog API object
	httpAPI := catalog.NewHTTPAPI(controller, config.Description, Version)

	commonHandlers := alice.New(
		context.ClearHandler,
	)

	// Append auth handler if enabled
	if config.Auth.Enabled {
		// Setup ticket validator
		v, err := validator.Setup(
			config.Auth.Provider,
			config.Auth.ProviderURL,
			config.Auth.ServiceID,
			config.Auth.BasicEnabled,
			config.Auth.Authz)
		if err != nil {
			return nil, nil, err
		}

		commonHandlers = commonHandlers.Append(v.Handler)
	}

	// Configure http httpAPI router
	r := newRouter()
	// Handlers
	r.get("/", commonHandlers.ThenFunc(httpAPI.List))
	r.post("/", commonHandlers.ThenFunc(httpAPI.Post))
	// Accept an id with zero or one slash: [^/]+/?[^/]*
	// -> [^/]+ one or more of anything but slashes /? optional slash [^/]* zero or more of anything but slashes
	r.get("/{id:[^/]+/?[^/]*}", commonHandlers.ThenFunc(httpAPI.Get))
	r.put("/{id:[^/]+/?[^/]*}", commonHandlers.ThenFunc(httpAPI.Put))
	r.delete("/{id:[^/]+/?[^/]*}", commonHandlers.ThenFunc(httpAPI.Delete))
	r.get("/{path}/{op}/{value:.*}", commonHandlers.ThenFunc(httpAPI.Filter))

	return r, controller.Stop, nil
}
package main

import (
	"flag"
	"log"
	"net/http"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/box/kube-exec-controller/pkg/controller"
	"github.com/box/kube-exec-controller/pkg/webhook"
)

func main() {
	certPath := flag.String("cert-path", "",
		"Path to the PEM-encoded TLS certificate",
	)
	keyPath := flag.String("key-path", "",
		"Path to the un-encrypted TLS key",
	)
	ttlSeconds := flag.Int("ttl-seconds", 600,
		"TTL (time-to-live) of interacted Pods before getting evicted by the controller",
	)
	port := flag.Int("port", 8443,
		"Port for the app to listen on",
	)
	apiServerURL := flag.String("api-server", "",
		"URL to K8s api-server, required if kube-proxy is not set up",
	)
	namespaceAllowlistRaw := flag.String("namespace-allowlist", "",
		"Comma separated list of namespaces that allow interaction without evicting their Pods",
	)
	podInteractChanSize := flag.Int("interact-chan-size", 500,
		"Buffer size of the channel for handling Pod interaction",
	)
	podExtendChanSize := flag.Int("extend-chan-size", 500,
		"Buffer size of the channel for handling Pod extension",
	)
	logLevel := flag.String("log-level", "info",
		"Log level. `debug`, `info`, `warn`, `error` are currently supported",
	)

	flag.Parse()

	// set up zap logging
	loggerCfg := zap.NewProductionConfig()
	loggerCfg.EncoderConfig.TimeKey = "timestamp"
	loggerCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	loggerCfg.Level.UnmarshalText([]byte(*logLevel))
	zapLogger, err := loggerCfg.Build()
	if err != nil {
		log.Fatalf("Cannot initialize zap logger. Error: %v", err)
	}

	zap.ReplaceGlobals(zapLogger)
	defer zapLogger.Sync()

	if *ttlSeconds < 0 {
		zap.L().Fatal("Flag '--ttl-seconds' cannot be set to a negative value.")
	}

	if *certPath == "" || *keyPath == "" {
		zap.L().Fatal("Flag '--cert-path' or '--key-path' is not set or set to an empty value.")
	}

	kubeClient, err := initKubeClient(*apiServerURL)
	if err != nil {
		zap.L().Fatal("Cannot initialize Kube client.", zap.Error(err))
	}

	// initialize controller service to handle Pod interaction and extension update
	controller.PodInteractionCh = make(chan controller.PodInteraction, *podInteractChanSize)
	controller.PodExtensionUpdateCh = make(chan controller.PodExtensionUpdate, *podExtendChanSize)
	contr := controller.NewController(kubeClient, *ttlSeconds)

	go func() {
		defer close(controller.PodInteractionCh)

		contr.CheckPodInteraction()
	}()

	go func() {
		defer close(controller.PodExtensionUpdateCh)

		contr.CheckPodExtensionUpdate()
	}()

	// initialize webhook server and start admitting incoming requests
	webhookServer, err := webhook.NewServer(*port, *certPath, *keyPath, *namespaceAllowlistRaw)
	if err != nil {
		zap.L().Fatal("Cannot initialize webhook server.", zap.Error(err))
	}

	err = webhookServer.Run()
	if err != nil && err != http.ErrServerClosed {
		zap.L().Fatal("Webhook server exited with an error.", zap.Error(err))
	}
}

func initKubeClient(apiServerURL string) (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	if len(apiServerURL) > 0 {
		zap.L().Info("Overriding api-server url in K8s client config.", zap.String("url", apiServerURL))
		config.Host = apiServerURL
	}

	return kubernetes.NewForConfig(config)
}

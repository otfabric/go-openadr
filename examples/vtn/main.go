// SPDX-License-Identifier: MIT

// Vtn runs a standalone VTN with its own token endpoint, for trying out
// clients and for interoperability tests. Tokens are lost when it stops, and
// so are its objects unless -store names a file to keep them in.
//
//	go run ./examples/vtn -addr 127.0.0.1:8080 \
//	    -client utility:utility-secret:bl -client site-42:site-42-secret:ven
//
// With -webhooks the VTN serves subscriptions and notifies their callback
// URLs, which have to be https and on public addresses; -insecure-webhooks
// also allows http and private addresses, for webhooks on the same machine.
// With -store as well, a notification that no webhook has confirmed when the
// VTN stops is sent when it starts again. -notify-reads makes a subscription
// to READ be notified of every read.
//
// With -rate the VTN takes that many requests a second from each client, and
// answers the rest 429 with Retry-After.
//
// With -mqtt the VTN publishes its notifications to the MQTT broker at that
// URL, as the user of -mqtt-user if given, and announces the broker to
// clients under -mqtt-public-url.
//
// With -websocket clients can be notified over a WebSocket, and with
// -resource-groups the VTN serves resource groups; neither is part of
// OpenADR 3.1.0. With -anonymous it answers requests without a token, for
// programs and events that have no targets. With -advertise it announces
// itself on the local network, for VENs that look for a VTN with DNS-SD.
//
// Clients are told where to get a token by URL. When they reach the VTN under
// another address than -addr, give that address with -public-url.
//
// It serves plain HTTP: put it behind a TLS-terminating proxy for anything
// but local use.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/discovery"
	"github.com/otfabric/go-openadr/notification/mqtt"
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// clientFlags collects the -client flags.
type clientFlags []auth.Client

func (c *clientFlags) String() string { return fmt.Sprintf("%d clients", len(*c)) }

func (c *clientFlags) Set(v string) error {
	parts := strings.Split(v, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return errors.New("want id:secret:role")
	}
	client := auth.Client{ID: parts[0], Secret: parts[1]}
	switch parts[2] {
	case "bl":
		client.Scopes = auth.BusinessLogicScopes()
	case "ven":
		client.Scopes = auth.VENScopes()
	default:
		return errors.New("role must be bl or ven")
	}
	*c = append(*c, client)
	return nil
}

func main() {
	var clients clientFlags
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	publicURL := flag.String("public-url", "", "URL under which clients reach the API (default http://<addr>)")
	webhooks := flag.Bool("webhooks", false, "serve subscriptions and notify their webhooks")
	insecure := flag.Bool("insecure-webhooks", false, "as -webhooks, and allow http and private addresses as callback URLs")
	store := flag.String("store", "", "file to keep the VTN's objects in across restarts (default: in memory)")
	mqttURL := flag.String("mqtt", "", "MQTT broker to publish notifications to, such as mqtt://127.0.0.1:1883")
	mqttUser := flag.String("mqtt-user", "", "user name for the broker; the password is read from $VTN_MQTT_PASSWORD")
	mqttPublic := flag.String("mqtt-public-url", "", "URL of the broker as clients reach it (default: the value of -mqtt)")
	websockets := flag.Bool("websocket", false, "notify over WebSockets (not part of OpenADR 3.1.0)")
	groups := flag.Bool("resource-groups", false, "serve resource groups (not part of OpenADR 3.1.0)")
	anonymous := flag.Bool("anonymous", false, "answer requests without a token, for what has no targets")
	advertise := flag.String("advertise", "", "announce the VTN on the local network under this name")
	rate := flag.Float64("rate", 0, "requests a second the VTN takes from each client, with twice as many at once (default: no limit)")
	reads := flag.Bool("notify-reads", false, "notify subscribers of the operation READ when an object is read")
	flag.Var(&clients, "client", "an OAuth2 client as id:secret:role, role being bl or ven; repeatable")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(clients) == 0 && !*anonymous {
		logger.Error("no -client given: nobody could authenticate")
		os.Exit(2)
	}
	if *publicURL == "" {
		*publicURL = "http://" + *addr
	}
	var sender *webhook.Sender
	if *webhooks || *insecure {
		sender = webhook.NewSender(webhook.SenderConfig{AllowInsecure: *insecure, Logger: logger})
	}
	o := options{addr: *addr, publicURL: strings.TrimSuffix(*publicURL, "/"), clients: clients, sender: sender, store: *store,
		websockets: *websockets, groups: *groups, anonymous: *anonymous, advertise: *advertise, rate: *rate, reads: *reads}
	if *mqttURL != "" {
		publisher, err := mqtt.NewPublisher(mqtt.PublisherConfig{Logger: logger, Broker: mqtt.Broker{
			URL: *mqttURL, ClientID: "go-openadr-vtn", Username: *mqttUser, Password: os.Getenv("VTN_MQTT_PASSWORD"),
		}})
		if err != nil {
			logger.Error("invalid -mqtt", "error", err)
			os.Exit(2)
		}
		if *mqttPublic == "" {
			*mqttPublic = *mqttURL
		}
		o.mqtt = &server.MQTTConfig{Publisher: publisher, URIs: []string{*mqttPublic}}
		o.closers = append(o.closers, publisher.Close)
	}
	if err := run(o, logger); err != nil {
		logger.Error("vtn stopped", "error", err)
		os.Exit(1)
	}
}

type options struct {
	addr, publicURL, store string
	clients                []auth.Client
	sender                 *webhook.Sender
	mqtt                   *server.MQTTConfig
	// What is not, or not only, OpenADR 3.1.0.
	websockets, groups, anonymous bool
	advertise                     string
	// rate limits each client to so many requests a second, when not 0.
	rate float64
	// reads notifies subscribers of READ.
	reads bool
	// closers stop background work at shutdown, within its time limit.
	closers []func(context.Context) error
}

func run(o options, logger *slog.Logger) error {
	addr, publicURL, clients, sender := o.addr, o.publicURL, o.clients, o.sender
	var store storage.Store = storage.NewMemory()
	// With a file, notifications to webhooks are kept in it until they
	// are delivered.
	var outbox storage.Outbox
	if o.store != "" {
		file, err := storage.OpenFile(o.store, storage.FileOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		store, outbox = file, file
	}
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: clients})
	if err != nil {
		return err
	}
	cfg := server.Config{
		Store:        store,
		Validator:    issuer,
		TokenURL:     publicURL + "/auth/token",
		TokenHandler: issuer,
		MQTT:         o.mqtt,
		Logger:       logger,

		ResourceGroups: o.groups,
		AllowAnonymous: o.anonymous,
		NotifyReads:    o.reads,
	}
	if o.rate > 0 {
		cfg.Limiter = server.NewRateLimiter(o.rate, int(2*o.rate)+1)
	}
	if o.websockets {
		cfg.WebSocket = &server.WebSocketConfig{}
	}
	if sender != nil {
		// A nil *webhook.Sender in the interface would not be nil.
		cfg.Webhooks = sender
		if outbox != nil {
			cfg.Outbox = outbox
		}
	}
	vtn, err := server.New(cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           vtn,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	if o.advertise != "" {
		ad, err := advertiseVTN(o, logger)
		if err != nil {
			return err
		}
		defer func() { _ = ad.Close() }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The WebSockets first: they are no longer the HTTP server's. This
		// also stops handing the sender what is still to be delivered.
		_ = vtn.Close()
		_ = srv.Shutdown(shutdown)
		// What is still queued gets the rest of the time.
		if sender != nil {
			_ = sender.Close(shutdown)
		}
		for _, closer := range o.closers {
			_ = closer(shutdown)
		}
	}()
	logger.Info("vtn listening", "addr", addr, "clients", len(clients))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	return nil
}

// advertiseVTN announces the VTN with DNS-SD, at the address clients reach
// it under.
func advertiseVTN(o options, logger *slog.Logger) (*discovery.Advertiser, error) {
	public, err := url.Parse(o.publicURL)
	if err != nil {
		return nil, err
	}
	port := public.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[public.Scheme]
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("-public-url has no port to announce: %w", err)
	}
	ad := discovery.Advertisement{Instance: o.advertise, Port: n, BasePath: public.Path, NoAuth: o.anonymous && len(o.clients) == 0, Logger: logger}
	// A name is announced as it is; an address is of this machine, and
	// announced under the machine's own name.
	if net.ParseIP(public.Hostname()) == nil {
		ad.LocalURL = o.publicURL
	} else if public.Scheme != "https" {
		// The default would say https.
		host, _ := os.Hostname()
		host, _, _ = strings.Cut(host, ".")
		ad.LocalURL = "http://" + net.JoinHostPort(host+".local", port) + public.Path
	}
	return discovery.Advertise(ad)
}

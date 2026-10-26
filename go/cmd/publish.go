package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jefflaplante/lux/internal/lux"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// pubLogger handles text or JSON logging with quiet mode support
type pubLogger struct {
	json  bool
	quiet bool
	w     io.Writer
}

func (l *pubLogger) info(msg string, fields map[string]any) {
	if l.quiet {
		return
	}
	if l.json {
		out := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339), "level": "info", "msg": msg}
		for k, v := range fields {
			out[k] = v
		}
		json.NewEncoder(l.w).Encode(out)
	} else {
		if len(fields) > 0 {
			fmt.Fprintf(l.w, "[%s] %s %v\n", time.Now().Format("15:04:05"), msg, fields)
		} else {
			fmt.Fprintf(l.w, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
		}
	}
}

func (l *pubLogger) status(msg string) {
	// Status messages always print (startup, shutdown) regardless of quiet
	if l.json {
		json.NewEncoder(l.w).Encode(map[string]any{
			"ts": time.Now().UTC().Format(time.RFC3339), "level": "info", "msg": msg,
		})
	} else {
		fmt.Fprintf(l.w, "%s\n", msg)
	}
}

func (l *pubLogger) warn(msg string, fields map[string]any) {
	// Data-integrity warnings always print regardless of quiet
	if l.json {
		out := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339), "level": "warn", "msg": msg}
		for k, v := range fields {
			out[k] = v
		}
		json.NewEncoder(l.w).Encode(out)
	} else {
		fmt.Fprintf(l.w, "[%s] warning: %s %v\n", time.Now().Format("15:04:05"), msg, fields)
	}
}

func (l *pubLogger) errorf(msg string, err error) {
	// Errors always print regardless of quiet
	if l.json {
		json.NewEncoder(l.w).Encode(map[string]any{
			"ts": time.Now().UTC().Format(time.RFC3339), "level": "error", "msg": msg, "error": err.Error(),
		})
	} else {
		fmt.Fprintf(l.w, "[%s] error: %s: %v\n", time.Now().Format("15:04:05"), msg, err)
	}
}

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Stream inverter data to MQTT (Solar Assistant compatible)",
	Long: `Connects to the inverter and publishes register data to an MQTT broker
using Solar Assistant-compatible topic names. This enables Home Assistant
integration and remote monitoring.

Topics are published under {prefix}/inverter_1/{metric}/state with
human-readable formatted values matching Solar Assistant output.

Health check topics:
  {prefix}/inverter_1/online/state    - "true" when connected, "false" on disconnect (LWT)
  {prefix}/inverter_1/last_seen/state - RFC3339 timestamp of the last fresh live-data
                                        (input register) publish; alert on its age

Data integrity: frames failing CRC are dropped, and an input block repeated
identically --stale-threshold times in a row is treated as a frozen dongle
cache — not published and not counted toward last_seen.

All flags can be set via environment variables with LUX_ prefix.`,
	RunE: runPublish,
}

func init() {
	publishCmd.Flags().String("broker", "", "MQTT broker URL (env: LUX_BROKER) [required]")
	publishCmd.Flags().String("prefix", "lux", "MQTT topic prefix (env: LUX_PREFIX)")
	publishCmd.Flags().String("username", "", "MQTT username (env: LUX_MQTT_USER)")
	publishCmd.Flags().String("password", "", "MQTT password (env: LUX_MQTT_PASS)")
	publishCmd.Flags().String("client-id", "", "MQTT client ID; default lux-publish-<hostname>-<pid> (env: LUX_MQTT_CLIENT_ID)")
	publishCmd.Flags().Bool("retain", true, "use MQTT retain flag (env: LUX_RETAIN)")
	publishCmd.Flags().Bool("quiet", false, "suppress per-packet logging (env: LUX_QUIET)")
	publishCmd.Flags().Bool("log-json", false, "output logs as JSON lines (env: LUX_LOG_JSON)")
	publishCmd.Flags().Int("stale-threshold", lux.DefaultStaleThreshold,
		"consecutive identical input frames treated as a frozen dongle cache; <2 disables (env: LUX_STALE_THRESHOLD)")

	// Bind to viper for env var support
	viper.BindPFlag("broker", publishCmd.Flags().Lookup("broker"))
	viper.BindPFlag("prefix", publishCmd.Flags().Lookup("prefix"))
	viper.BindPFlag("mqtt_user", publishCmd.Flags().Lookup("username"))
	viper.BindPFlag("mqtt_pass", publishCmd.Flags().Lookup("password"))
	viper.BindPFlag("mqtt_client_id", publishCmd.Flags().Lookup("client-id"))
	viper.BindPFlag("retain", publishCmd.Flags().Lookup("retain"))
	viper.BindPFlag("quiet", publishCmd.Flags().Lookup("quiet"))
	viper.BindPFlag("log_json", publishCmd.Flags().Lookup("log-json"))
	viper.BindPFlag("stale_threshold", publishCmd.Flags().Lookup("stale-threshold"))

	rootCmd.AddCommand(publishCmd)
}

func runPublish(cmd *cobra.Command, args []string) error {
	broker := viper.GetString("broker")
	if broker == "" {
		return fmt.Errorf("--broker or LUX_BROKER is required")
	}
	prefix := viper.GetString("prefix")
	username := viper.GetString("mqtt_user")
	password := viper.GetString("mqtt_pass")
	retain := viper.GetBool("retain")
	quiet := viper.GetBool("quiet")
	logJSON := viper.GetBool("log_json")
	staleThreshold := viper.GetInt("stale_threshold")
	clientID := viper.GetString("mqtt_client_id")
	if clientID == "" {
		hostname, _ := os.Hostname()
		clientID = defaultClientID(hostname, os.Getpid())
	}

	log := &pubLogger{json: logJSON, quiet: quiet, w: os.Stderr}

	pub := &publisher{
		prefix: prefix,
		retain: retain,
		log:    log,
		stale:  lux.NewStaleDetector(staleThreshold, lux.WatchedInputRegisters()),
	}

	// Connect to MQTT broker (stays connected across inverter reconnects).
	// OnConnect fires on every (re)connect and replaces the retained LWT
	// "false" the broker published while we were away.
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetWill(pub.onlineTopic(), "false", 1, true).
		SetOnConnectHandler(func(mqtt.Client) { pub.onConnect() })

	if username != "" {
		opts.SetUsername(username)
	}
	if password != "" {
		opts.SetPassword(password)
	}

	mqttClient := mqtt.NewClient(opts)
	pub.mqtt = mqttClient
	tok := mqttClient.Connect()
	if !tok.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("MQTT connect timeout")
	}
	if tok.Error() != nil {
		return fmt.Errorf("MQTT connect: %w", tok.Error())
	}

	host := viper.GetString("host")
	port := viper.GetInt("port")

	log.status(fmt.Sprintf("Connected: inverter=%s:%d broker=%s prefix=%s client_id=%s", host, port, broker, prefix, clientID))
	log.status("Publishing (Ctrl-C to stop)...")

	// Shutdown signal handling. The handler only closes the active inverter
	// connection; all MQTT publishing stays on this goroutine.
	gate := newShutdownGate()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.status("Shutting down...")
		// Restore default signal handling - second Ctrl-C will force exit
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		gate.shutdown()
	}()

	// finish runs once Listen has returned, so no packet callback is in
	// flight: publish the final offline state, wait for it, then let paho
	// drain in-flight messages and disconnect.
	finish := func() error {
		pub.setInverterUp(false)
		pub.publishOnline()
		mqttClient.Disconnect(250)
		log.status(fmt.Sprintf("Done. %d total publishes.", pub.totalPublished))
		return nil
	}

	// Reconnect loop with exponential backoff
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		// Check shutdown before connecting
		select {
		case <-gate.done:
			return finish()
		default:
		}

		client, err := newClient(cmd)
		if err != nil {
			log.errorf("inverter connect failed", err)
			pub.setInverterUp(false)
			pub.publishOnline()
			log.status(fmt.Sprintf("Retrying in %v...", backoff))

			select {
			case <-gate.done:
				return finish()
			case <-time.After(backoff):
			}

			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// Adopt the client unless a signal arrived while we were dialing
		if !gate.register(client) {
			client.Close()
			return finish()
		}

		// Connected - reset backoff
		backoff = 1 * time.Second
		pub.setInverterUp(true)
		pub.publishOnline()

		listenErr := client.Listen(func(pkt *lux.Packet) {
			pub.noteBadCRC(client.DroppedBadCRC())
			if pkt.TCPFunction == lux.FuncHeartbeat {
				pub.publishOnline()
				return
			}
			pub.handlePacket(pkt)
		})

		gate.unregister()
		client.Close()
		pub.setInverterUp(false)

		// Check if this was a shutdown
		select {
		case <-gate.done:
			return finish()
		default:
		}

		if listenErr != nil {
			log.errorf("inverter connection lost", listenErr)
			pub.publishOnline()
			log.status("Reconnecting...")
		}
	}
}

// defaultClientID returns a per-process MQTT client ID. A fixed ID made two
// publishers kick each other off the broker in a reconnect loop.
func defaultClientID(hostname string, pid int) string {
	if hostname == "" {
		hostname = "unknown"
	}
	return fmt.Sprintf("lux-publish-%s-%d", hostname, pid)
}

// shutdownGate serializes adopting a new inverter connection with shutdown,
// so a signal that lands while dialing can't leave a fresh connection
// blocked in Listen with nobody left to close it.
type shutdownGate struct {
	mu      sync.Mutex
	closing bool
	active  io.Closer
	done    chan struct{}
}

func newShutdownGate() *shutdownGate {
	return &shutdownGate{done: make(chan struct{})}
}

// register makes c the active connection. It returns false if shutdown has
// begun, in which case the caller must close c itself.
func (g *shutdownGate) register(c io.Closer) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.active = c
	return true
}

// unregister clears the active connection.
func (g *shutdownGate) unregister() {
	g.mu.Lock()
	g.active = nil
	g.mu.Unlock()
}

// shutdown closes the active connection (unblocking Listen) and closes done.
// Safe to call more than once.
func (g *shutdownGate) shutdown() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return
	}
	g.closing = true
	if g.active != nil {
		g.active.Close()
	}
	close(g.done)
}

// dropLogInterval rate-limits repeated data-integrity warnings.
const dropLogInterval = time.Minute

// publishTimeout bounds how long a single publish token is awaited.
const publishTimeout = 5 * time.Second

// mqttPublisher is the subset of mqtt.Client the publisher uses.
type mqttPublisher interface {
	Publish(topic string, qos byte, retained bool, payload interface{}) mqtt.Token
}

// publisher turns decoded inverter packets into MQTT messages.
type publisher struct {
	mqtt   mqttPublisher
	prefix string
	retain bool
	log    *pubLogger
	stale  *lux.StaleDetector
	now    func() time.Time // nil = time.Now

	totalPublished int
	staleSkipped   uint64
	lastStaleLog   time.Time
	badCRCBase     uint64 // bad-CRC total already logged for this connection
	lastCRCLog     time.Time

	inverterUp atomic.Bool // inverter connection state, for online/state

	// Publish failures; guarded by failMu since onConnect runs on paho's goroutine.
	failMu        sync.Mutex
	publishFailed uint64
	lastFailLog   time.Time
}

func (p *publisher) onlineTopic() string { return p.prefix + "/inverter_1/online/state" }

// setInverterUp records whether the inverter connection is live.
func (p *publisher) setInverterUp(up bool) { p.inverterUp.Store(up) }

// publishOnline publishes the current inverter state to online/state,
// retained at QoS 1, and reports whether the broker accepted it.
func (p *publisher) publishOnline() bool {
	val := "false"
	if p.inverterUp.Load() {
		val = "true"
	}
	topic := p.onlineTopic()
	return p.await(p.mqtt.Publish(topic, 1, true, val), topic)
}

// onConnect runs on every MQTT (re)connect. After an unclean disconnect the
// broker holds our retained LWT "false"; republish the real state.
func (p *publisher) onConnect() {
	p.publishOnline()
}

// await waits for a publish token and reports success. Failures and
// timeouts are counted and logged at most once per dropLogInterval.
func (p *publisher) await(tok mqtt.Token, topic string) bool {
	var err error
	if !tok.WaitTimeout(publishTimeout) {
		err = fmt.Errorf("timed out after %v", publishTimeout)
	} else {
		err = tok.Error()
	}
	if err == nil {
		return true
	}
	p.failMu.Lock()
	defer p.failMu.Unlock()
	p.publishFailed++
	if p.occasionally(&p.lastFailLog) {
		p.log.warn("MQTT publish failed", map[string]any{
			"topic": topic, "error": err.Error(), "failed_total": p.publishFailed,
		})
	}
	return false
}

func (p *publisher) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// occasionally reports whether a rate-limited log line may be written now.
func (p *publisher) occasionally(last *time.Time) bool {
	now := p.clock()
	if last.IsZero() || now.Sub(*last) >= dropLogInterval {
		*last = now
		return true
	}
	return false
}

// noteBadCRC logs growth in the client's bad-CRC drop count, at most once per
// dropLogInterval. total comes from the current connection's client; each new
// Client starts its counter at zero, so a decrease is a reconnect, not a
// regression — rebase instead of suppressing forever.
func (p *publisher) noteBadCRC(total uint64) {
	if total < p.badCRCBase {
		p.badCRCBase = total
		return
	}
	if total == p.badCRCBase {
		return
	}
	if p.occasionally(&p.lastCRCLog) {
		p.log.warn("dropped frames with bad CRC", map[string]any{"new": total - p.badCRCBase})
		p.badCRCBase = total
	}
}

// handlePacket publishes one register packet. Frozen (stale) input frames are
// skipped entirely and do not refresh last_seen, so its age reveals a stuck
// dongle. last_seen tracks live input data only; config frames don't touch it.
func (p *publisher) handlePacket(pkt *lux.Packet) {
	var topicMap map[uint16][]lux.MQTTMapping
	switch pkt.RegisterType {
	case "input":
		topicMap = lux.InputMQTTTopics
	case "holding":
		topicMap = lux.HoldingMQTTTopics
	default:
		return
	}

	if p.stale.Observe(pkt) {
		p.staleSkipped++
		if p.occasionally(&p.lastStaleLog) {
			p.log.warn("skipping frozen frame (identical repeat)", map[string]any{
				"start": pkt.StartRegister, "count": pkt.RegisterCount, "skipped_total": p.staleSkipped,
			})
		}
		return
	}

	type pending struct {
		tok   mqtt.Token
		topic string
	}
	var sent []pending
	send := func(topic, val string) {
		full := p.prefix + "/" + topic + "/state"
		sent = append(sent, pending{p.mqtt.Publish(full, 0, p.retain, val), full})
	}

	// Individual register topics
	for reg := pkt.StartRegister; reg < pkt.StartRegister+pkt.RegisterCount; reg++ {
		raw, ok := pkt.Registers[reg]
		if !ok {
			continue
		}
		for _, m := range topicMap[reg] {
			val := fmt.Sprintf("%d", raw)
			if m.Format != nil {
				val = m.Format(raw)
			}
			send(m.Topic, val)
		}
	}

	// Bitmask flags from register 21
	if pkt.RegisterType == "holding" {
		if raw, ok := pkt.Registers[21]; ok {
			for bit, topic := range lux.MasterFlagTopics {
				val := "false"
				if raw&(1<<bit) != 0 {
					val = "true"
				}
				send(topic, val)
			}
		}
	}

	// Computed topics for input registers
	if pkt.RegisterType == "input" {
		for topic, val := range lux.ComputedTopics(pkt.Registers) {
			send(topic, val)
		}
	}

	// Count only publishes the client actually accepted
	packetPublished := 0
	for _, s := range sent {
		if p.await(s.tok, s.topic) {
			packetPublished++
		}
	}
	p.totalPublished += packetPublished

	if pkt.RegisterType == "input" && packetPublished > 0 {
		topic := p.prefix + "/inverter_1/last_seen/state"
		p.await(p.mqtt.Publish(topic, 0, true, p.clock().UTC().Format(time.RFC3339)), topic)
	}

	p.log.info("published", map[string]any{
		"type":  pkt.RegisterType,
		"start": pkt.StartRegister,
		"end":   pkt.StartRegister + pkt.RegisterCount - 1,
		"count": packetPublished,
		"total": p.totalPublished,
	})
}

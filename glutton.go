package glutton

import (
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kung-foo/freki"
	"github.com/mushorg/glutton/producer"
	uuid "github.com/satori/go.uuid"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// Glutton struct
type Glutton struct {
	id               uuid.UUID
	Logger           *zap.Logger
	Processor        *freki.Processor
	rules            []*freki.Rule
	Producer         *producer.Producer
	protocolHandlers map[string]protocolHandlerFunc
	telnetProxy      *telnetProxy
	sshProxy         *sshProxy
	ctx              context.Context
	cancel           context.CancelFunc
}

type protocolHandlerFunc func(ctx context.Context, conn net.Conn) error

func (g *Glutton) initConfig() error {
	viper.SetConfigName("conf")
	viper.AddConfigPath(viper.GetString("confpath"))
	if err := viper.ReadInConfig(); err != nil {
		return err
	}
	// If no config is found, use the defaults
	viper.SetDefault("glutton_server", 5000)
	viper.SetDefault("max_tcp_payload", 4096)
	viper.SetDefault("rules_path", "rules/rules.yaml")
	g.Logger.Debug("configuration loaded successfully", zap.String("reporter", "glutton"))
	return nil
}

// New creates a new Glutton instance
func New() (*Glutton, error) {
	g := &Glutton{}
	g.protocolHandlers = make(map[string]protocolHandlerFunc, 0)
	if err := g.makeID(); err != nil {
		return nil, err
	}
	g.Logger = NewLogger(g.id.String())

	// Loading the configuration
	g.Logger.Info("Loading configurations from: config/conf.yaml", zap.String("reporter", "glutton"))
	if err := g.initConfig(); err != nil {
		return nil, err
	}

	rulesPath := viper.GetString("rules_path")
	rulesFile, err := os.Open(rulesPath)
	defer rulesFile.Close()
	if err != nil {
		return nil, err
	}

	g.rules, err = freki.ReadRulesFromFile(rulesFile)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// Init initializes freki and handles
func (g *Glutton) Init() error {
	ctx := context.Background()
	g.ctx, g.cancel = context.WithCancel(ctx)

	gluttonServerPort := uint(viper.GetInt("glutton_server"))

	// Initiate the freki processor
	var err error
	g.Processor, err = freki.New(viper.GetString("interface"), g.rules, nil)
	if err != nil {
		return err
	}

	// Initiating glutton server
	g.Processor.AddServer(freki.NewUserConnServer(gluttonServerPort))
	// Initiating log producers
	if viper.GetBool("producers.enabled") {
		g.Producer, err = producer.New(g.id.String())
		if err != nil {
			return err
		}
	}
	// Initiating protocol handlers
	g.mapProtocolHandlers()
	g.registerHandlers()

	return g.Processor.Init()
}

// Start the packet processor
func (g *Glutton) Start() error {
	quit := make(chan struct{}) // stop monitor on shutdown
	defer func() {
		quit <- struct{}{}
		g.Shutdown()
	}()

	g.startMonitor(quit)
	return g.Processor.Start()
}

func (g *Glutton) makeID() error {
	fileName := "glutton.id"
	filePath := filepath.Join(viper.GetString("var-dir"), fileName)
	if err := os.MkdirAll(viper.GetString("var-dir"), 0744); err != nil {
		return err
	}
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		g.id = uuid.NewV4()
		if err := ioutil.WriteFile(filePath, g.id.Bytes(), 0744); err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		buff, err := ioutil.ReadAll(f)
		if err != nil {
			return err
		}
		g.id, err = uuid.FromBytes(buff)
		if err != nil {
			return err
		}
	}
	return nil
}

// registerHandlers register protocol handlers to glutton_server
func (g *Glutton) registerHandlers() {
	for _, rule := range g.rules {

		if rule.Type == "conn_handler" && rule.Target != "" {

			var handler string

			switch rule.Name {
			case "proxy_tcp":
				handler = rule.Name
				g.protocolHandlers[rule.Target] = g.protocolHandlers[handler]
				delete(g.protocolHandlers, handler)
				handler = rule.Target
				break
			case "proxy_ssh":
				handler = rule.Name
				err := g.NewSSHProxy(rule.Target)
				if err != nil {
					g.Logger.Error(fmt.Sprintf("[ssh.prxy] failed to initialize SSH proxy"))
					continue
				}
				rule.Target = handler
				break
			case "proxy_telnet":
				handler = rule.Name
				err := g.NewTelnetProxy(rule.Target)
				if err != nil {
					g.Logger.Error(fmt.Sprint("[telnet.prxy] failed to initialize TELNET proxy"))
					continue
				}
				rule.Target = handler
				break
			default:
				handler = rule.Target
				break
			}

			if g.protocolHandlers[handler] == nil {
				g.Logger.Warn(fmt.Sprintf("[glutton ] no handler found for %v protocol", handler))
				continue
			}

			g.Processor.RegisterConnHandler(handler, func(conn net.Conn, md *freki.Metadata) error {
				host, port, err := net.SplitHostPort(conn.RemoteAddr().String())
				if err != nil {
					return err
				}

				if md == nil {
					g.Logger.Debug(fmt.Sprintf("[glutton ] connection not tracked: %s:%s", host, port))
					return nil
				}
				g.Logger.Debug(
					fmt.Sprintf("[glutton ] new connection: %s:%s -> %d", host, port, md.TargetPort),
					zap.String("host", host),
					zap.String("src_port", port),
					zap.String("dest_port", strconv.Itoa(int(md.TargetPort))),
					zap.String("handler", handler),
				)

				if g.Producer != nil {
					if err := g.Producer.Log(conn, md, nil); err != nil {
						g.Logger.Error(fmt.Sprintf("[glutton ] error: %v", err))
						return err
					}
				}

				done := make(chan struct{})
				go g.closeOnShutdown(conn, done)
				if err = conn.SetDeadline(time.Now().Add(time.Duration(viper.GetInt("conn_timeout")) * time.Second)); err != nil {
					return err
				}
				ctx := g.contextWithTimeout(72)
				err = g.protocolHandlers[handler](ctx, conn)
				done <- struct{}{}
				return err
			})
		}
	}
}

// ConnectionByFlow returns connection metadata by connection key
func (g *Glutton) ConnectionByFlow(ckey [2]uint64) *freki.Metadata {
	return g.Processor.Connections.GetByFlow(ckey)
}

func (g *Glutton) Produce(conn net.Conn, md *freki.Metadata, payload []byte) error {
	return g.Producer.Log(conn, md, payload)
}

// Shutdown the packet processor
func (g *Glutton) Shutdown() error {
	defer g.Logger.Sync()
	g.cancel() // close all connection

	/** TODO:
	 ** May be there exist a better way to wait for all connections to be closed but I am unable
	 ** to find. The only link we have between program and goroutines is context.
	 ** context.cancel() signal routines to abandon their work and does not wait
	 ** for the work to stop. And in any case if fails then there will be definitely a
	 ** goroutine leak. May be it is possible in future when we have connection counter so we can keep
	 ** that counter synchronized with number of goroutines (connections) with help of context and on
	 ** shutdown we wait until counter goes to zero.
	 */

	time.Sleep(2 * time.Second)
	return g.Processor.Shutdown()
}

// OnErrorClose prints the error, closes the connection and exits
func (g *Glutton) onErrorClose(err error, conn net.Conn) {
	if err != nil {
		g.Logger.Error(fmt.Sprintf("[glutton ] error: %v", err))
		err = conn.Close()
		if err != nil {
			g.Logger.Error(fmt.Sprintf("[glutton ] error: %v", err))
		}
	}
}

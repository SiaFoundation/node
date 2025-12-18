package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/testutil"
	"go.sia.tech/node/api"
	"go.sia.tech/node/internal/ip"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// initLog initializes the logger with the specified settings.
func initLog(showColors bool, logLevel zap.AtomicLevel) *zap.Logger {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.RFC3339TimeEncoder
	cfg.EncodeDuration = zapcore.StringDurationEncoder

	if showColors {
		cfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg.EncodeLevel = zapcore.CapitalLevelEncoder
	}

	cfg.StacktraceKey = ""
	cfg.CallerKey = ""
	encoder := zapcore.NewConsoleEncoder(cfg)
	core := zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), logLevel)
	log := zap.New(core, zap.AddCaller())

	zap.RedirectStdLog(log)
	return log
}

func main() {
	var (
		networkName string
		dir         string
		level       zap.AtomicLevel
		syncerPort  uint
	)

	flag.StringVar(&networkName, "network", "mainnet", "the network to use (mainnet, zen)")
	flag.StringVar(&dir, "dir", ".", "the directory to store data")
	flag.UintVar(&syncerPort, "port", 9981, "the port to listen for syncer connections on")
	flag.TextVar(&level, "log.level", zap.NewAtomicLevelAt(zap.InfoLevel), "the log level")
	flag.Parse()

	log := initLog(runtime.GOOS != "windows", level)

	if syncerPort == 0 || syncerPort > 65535 {
		log.Panic("invalid syncer port", zap.Uint("port", syncerPort))
	}

	var network *consensus.Network
	var genesis types.Block
	var bootstrapPeers []string
	switch networkName {
	case "mainnet":
		bootstrapPeers = syncer.MainnetBootstrapPeers
		network, genesis = chain.Mainnet()
	case "zen":
		bootstrapPeers = syncer.ZenBootstrapPeers
		network, genesis = chain.TestnetZen()
	default:
		log.Panic("unknown network", zap.String("name", networkName))
	}
	genesisID := genesis.ID()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Panic("failed to create data directory", zap.Error(err))
	}

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(dir, "consensus.db"))
	if err != nil {
		log.Panic("failed to open boltdb", zap.Error(err))
	}
	defer bdb.Close()

	dbstore, tipState, err := chain.NewDBStore(bdb, network, genesis, chain.NewZapMigrationLogger(log.Named("chain")))
	if err != nil {
		log.Panic("failed to create chain store", zap.Error(err))
	}
	cm := chain.NewManager(dbstore, tipState, chain.WithLog(log.Named("chain")))
	log.Info("using network", zap.String("name", networkName), zap.Stringer("genesisID", genesisID), zap.Stringer("tip", cm.Tip()))

	stop := cm.OnReorg(func(tip types.ChainIndex) {
		log.Info("chain reorg", zap.Stringer("tip", tip))
	})
	defer stop()

	syncerOpts := []syncer.Option{
		syncer.WithMaxInflightRPCs(1e6), syncer.WithMaxInboundPeers(1e6),
	}

	ps := testutil.NewEphemeralPeerStore()
	for _, addr := range bootstrapPeers {
		ps.AddPeer(addr)
	}

	ip4, err := ip.Getv4()
	if err != nil {
		log.Warn("failed to determine IPv4 address", zap.Error(err))
	} else {
		log.Info("determined IPv4 address", zap.String("ip", ip4.String()))
		netAddress := net.JoinHostPort(ip4.String(), strconv.Itoa(int(syncerPort)))
		l, err := net.Listen("tcp4", fmt.Sprintf(":%d", syncerPort))
		if err != nil {
			log.Panic("failed to listen on IPv4 address", zap.Error(err))
		}
		defer l.Close()

		header := gateway.Header{
			GenesisID:  genesisID,
			UniqueID:   gateway.GenerateUniqueID(),
			NetAddress: netAddress,
		}
		log.Info("listening for syncer connections on IPv4", zap.String("address", netAddress))
		s := syncer.New(l, cm, ps, header, syncerOpts...)
		defer s.Close()
		go s.Run()
	}

	ip6, err := ip.Getv6()
	if err != nil {
		log.Warn("failed to determine IPv6 address", zap.Error(err))
	} else {
		log.Info("determined IPv6 address", zap.String("ip", ip6.String()))
		netAddress := net.JoinHostPort(ip6.String(), strconv.Itoa(int(syncerPort)))
		l, err := net.Listen("tcp6", fmt.Sprintf(":%d", syncerPort))
		if err != nil {
			log.Panic("failed to listen on IPv6 address", zap.Error(err))
		}
		defer l.Close()

		header := gateway.Header{
			GenesisID:  genesisID,
			UniqueID:   gateway.GenerateUniqueID(),
			NetAddress: netAddress,
		}
		log.Info("listening for syncer connections on IPv6", zap.String("address", netAddress))
		s := syncer.New(l, cm, ps, header, syncerOpts...)
		defer s.Close()
		go s.Run()
	}

	l, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Panic("failed to listen for API connections", zap.Error(err))
	}
	defer l.Close()

	s := &http.Server{
		Handler:           api.NewHandler(cm),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
	}
	go func() {
		log.Info("listening for API connections on :8080")
		if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Panic("API server failed", zap.Error(err))
		}
	}()

	<-ctx.Done()
}

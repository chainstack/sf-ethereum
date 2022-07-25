// Copyright 2021 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"fmt"
	"github.com/Shopify/sarama"
	"github.com/klauspost/compress/zstd"
	"github.com/streamingfast/sf-ethereum/tools"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dgrpc"
	"github.com/streamingfast/dlauncher/launcher"
	"github.com/streamingfast/logging"
	nodeManager "github.com/streamingfast/node-manager"
	nodeManagerApp "github.com/streamingfast/node-manager/app/node_manager"
	nodeMindReaderApp "github.com/streamingfast/node-manager/app/node_mindreader"
	"github.com/streamingfast/node-manager/metrics"
	"github.com/streamingfast/node-manager/operator"
	nodemanager "github.com/streamingfast/sf-ethereum/node-manager"
	"github.com/streamingfast/sf-ethereum/node-manager/geth"
	"github.com/streamingfast/sf-ethereum/node-manager/openeth"
	"go.uber.org/zap"
)

var nodeLogger, _ = logging.PackageLogger("node", "github.com/streamingfast/sf-ethereum/node")
var nodeGethLogger, _ = logging.PackageLogger("node.geth", "github.com/streamingfast/sf-ethereum/node/geth", DefaultLevelInfo)
var nodeOpenEthereumLogger, _ = logging.PackageLogger("node.openethereum", "github.com/streamingfast/sf-ethereum/node/open-ethereum", DefaultLevelInfo)

var mindreaderLogger, _ = logging.PackageLogger("mindreader", "github.com/streamingfast/sf-ethereum/mindreader")
var mindreaderGethLogger, _ = logging.PackageLogger("mindreader.geth", "github.com/streamingfast/sf-ethereum/mindreader/geth", DefaultLevelInfo)
var mindreaderOpenEthereumLogger, _ = logging.PackageLogger("mindreader.open-ethereum", "github.com/streamingfast/sf-ethereum/mindreader/open-ethereum", DefaultLevelInfo)

func registerNodeApp(backupModuleFactories map[string]operator.BackupModuleFactory) {
	launcher.RegisterApp(zlog, &launcher.AppDef{
		ID:            "node",
		Title:         "Ethereum Node",
		Description:   "Ethereum node with built-in operational manager",
		RegisterFlags: registerEthereumNodeFlags,
		InitFunc: func(runtime *launcher.Runtime) error {
			return nil
		},
		FactoryFunc: nodeFactoryFunc(false, backupModuleFactories)})
}

func nodeFactoryFunc(isMindreader bool, backupModuleFactories map[string]operator.BackupModuleFactory) func(*launcher.Runtime) (launcher.App, error) {
	return func(runtime *launcher.Runtime) (launcher.App, error) {
		appLogger := nodeLogger
		if isMindreader {
			appLogger = mindreaderLogger
		}

		sfDataDir := runtime.AbsDataDir

		nodePath,
			blockmetaAddr,
			networkID,
			nodeType,
			nodeDataDir,
			nodeIPCPath,
			debugDeepMind,
			logToZap,
			managerAPIAddress,
			readinessMaxLatency,
			nodeEnforcePeers,
			bootstrapDataURL,
			backupConfigs,
			shutdownDelay,
			nodeArguments,
			err := parseCommonNodeFlags(appLogger, sfDataDir, isMindreader)
		if err != nil {
			return nil, err
		}

		prefix := "node"
		if isMindreader {
			prefix = "mindreader-node"
		}
		metricsAndReadinessManager := buildMetricsAndReadinessManager(prefix, readinessMaxLatency)

		nodeLogger := getSupervisedProcessLogger(isMindreader, nodeType)

		superviser, err := buildSuperviser(
			metricsAndReadinessManager,
			nodeType,
			nodePath,
			nodeIPCPath,
			nodeDataDir,
			nodeArguments,
			nodeEnforcePeers,
			appLogger,
			nodeLogger,
			logToZap,
			debugDeepMind,
		)
		if err != nil {
			return nil, err
		}

		tracker := runtime.Tracker

		if blockmetaAddr != "" {
			tracker = runtime.Tracker.Clone()
			tracker.AddGetter(bstream.NetworkLIBTarget, bstream.NetworkLIBBlockRefGetter(blockmetaAddr))
		}

		var bootstrapper operator.Bootstrapper
		if bootstrapDataURL != "" {
			if nodeType != "geth" {
				return nil, fmt.Errorf("feature bootstrap-data-url only supported for node type geth")
			}

			switch {
			case strings.HasSuffix(bootstrapDataURL, "tar.zst") || strings.HasSuffix(bootstrapDataURL, "tar.zstd"):
				bootstrapper = geth.NewTarballBootstrapper(bootstrapDataURL, nodeDataDir, nodeLogger)
			case strings.HasSuffix(bootstrapDataURL, "json"):
				// special bootstrap case
				bootstrapArgs, err := buildNodeArguments(appLogger, networkID, nodeDataDir, nodeIPCPath, "", nodeType, "bootstrap")
				if err != nil {
					return nil, fmt.Errorf("cannot build node bootstrap arguments")
				}
				bootstrapper = geth.NewGenesisBootstrapper(nodeDataDir, bootstrapDataURL, nodePath, bootstrapArgs, nodeLogger)
			default:
				return nil, fmt.Errorf("bootstrap-data-url should point to either an archive ending in '.tar.zstd' or a genesis file ending in '.json', not %s", bootstrapDataURL)
			}
		}

		chainOperator, err := buildChainOperator(
			superviser,
			metricsAndReadinessManager,
			shutdownDelay,
			bootstrapper,
			appLogger,
		)
		if err != nil {
			return nil, err
		}

		backupModules, backupSchedules, err := operator.ParseBackupConfigs(appLogger, backupConfigs, backupModuleFactories)
		if err != nil {
			return nil, fmt.Errorf("parsing backup configs: %w", err)
		}

		appLogger.Info("backup config",
			zap.Strings("config", backupConfigs),
			zap.Int("backup_module_count", len(backupModules)),
			zap.Int("backup_schedule_count", len(backupSchedules)),
		)

		for name, mod := range backupModules {
			appLogger.Info("registering backup module", zap.String("name", name), zap.Any("module", mod))
			err := chainOperator.RegisterBackupModule(name, mod)
			if err != nil {
				return nil, fmt.Errorf("unable to register backup module %s: %w", name, err)
			}
			appLogger.Info("backup module registered", zap.String("name", name), zap.Any("module", mod))
		}

		for _, sched := range backupSchedules {
			chainOperator.RegisterBackupSchedule(sched)
		}

		if !isMindreader {
			return nodeManagerApp.New(&nodeManagerApp.Config{
				ManagerAPIAddress: managerAPIAddress,
			}, &nodeManagerApp.Modules{
				Operator:                   chainOperator,
				MetricsAndReadinessManager: metricsAndReadinessManager,
			}, appLogger), nil
		} else {
			GRPCAddr := viper.GetString("mindreader-node-grpc-listen-addr")
			oneBlockStoreURL := MustReplaceDataDir(sfDataDir, viper.GetString("common-oneblock-store-url"))
			mergedBlockStoreURL := MustReplaceDataDir(sfDataDir, viper.GetString("common-blocks-store-url"))
			workingDir := MustReplaceDataDir(sfDataDir, viper.GetString("mindreader-node-working-dir"))
			mergeAndStoreDirectly := viper.GetBool("mindreader-node-merge-and-store-directly")
			mergeThresholdBlockAge := viper.GetDuration("mindreader-node-merge-threshold-block-age")
			if mergeThresholdBlockAge < 30*time.Second {
				return nil, fmt.Errorf("invalid value for mindreader-node-merge-threshold-block-age: should be more than 30 seconds. Use --mindreader-node-merge-and-store-directly to merge everything directly")
			}
			batchStartBlockNum := viper.GetUint64("mindreader-node-start-block-num")
			batchStopBlockNum := viper.GetUint64("mindreader-node-stop-block-num")
			failOnNonContiguousBlock := false //FIXME ?
			waitTimeForUploadOnShutdown := viper.GetDuration("mindreader-node-wait-upload-complete-on-shutdown")
			oneBlockFileSuffix := viper.GetString("mindreader-node-oneblock-suffix")
			blocksChanCapacity := viper.GetInt("mindreader-node-blocks-chan-capacity")
			gs := dgrpc.NewServer(dgrpc.WithLogger(appLogger))

			config := sarama.NewConfig()
			config.Producer.MaxMessageBytes = 5 * 1_000_000
			config.Producer.Return.Errors = true
			config.Producer.Return.Successes = true
			config.Producer.Compression = sarama.CompressionZSTD
			config.Producer.CompressionLevel = int(zstd.SpeedDefault)

			config.ClientID = viper.GetString("mindreader-node-kafka-client-id")
			config.Version, err = sarama.ParseKafkaVersion(viper.GetString("mindreader-node-kafka-version"))
			if err != nil {
				appLogger.Warn("no kafka version provided")
			}
			if err = config.Validate(); err != nil {
				return nil, fmt.Errorf("validating kafka client config: %w", err)
			}

			producer, err := sarama.NewAsyncProducer(viper.GetStringSlice("mindreader-node-kafka-addrs"), config)
			if err != nil {
				return nil, fmt.Errorf("initializing kafka producer: %w", err)
			}

			mindreaderPlugin, err := getMindreaderLogPlugin(
				oneBlockStoreURL,
				mergedBlockStoreURL,
				workingDir,
				mergeAndStoreDirectly,
				mergeThresholdBlockAge,
				batchStartBlockNum,
				batchStopBlockNum,
				failOnNonContiguousBlock,
				waitTimeForUploadOnShutdown,
				oneBlockFileSuffix,
				blocksChanCapacity,
				chainOperator.Shutdown,
				metricsAndReadinessManager,
				tracker,
				gs,
				tools.NewKafkaProducer(viper.GetString("mindreader-node-kafka-topic"), appLogger, producer.Input()),
				appLogger,
			)
			if err != nil {
				return nil, err
			}

			superviser.RegisterLogPlugin(mindreaderPlugin)

			trxPoolLogPlugin := nodemanager.NewTrxPoolLogPlugin(appLogger)
			superviser.RegisterLogPlugin(trxPoolLogPlugin)
			trxPoolLogPlugin.RegisterServices(gs)

			app := nodeMindReaderApp.New(&nodeMindReaderApp.Config{
				ManagerAPIAddress: managerAPIAddress,
				GRPCAddr:          GRPCAddr,
			}, &nodeMindReaderApp.Modules{
				Operator:                   chainOperator,
				MetricsAndReadinessManager: metricsAndReadinessManager,
				GrpcServer:                 gs,
			}, appLogger)

			return tools.NewLauncherWrapper(app, appLogger, producer), nil
		}
	}
}

func getSupervisedProcessLogger(isMindreader bool, nodeType string) *zap.Logger {
	switch nodeType {
	case "geth":
		if isMindreader {
			return mindreaderGethLogger
		} else {
			return nodeGethLogger
		}
	case "openethereum":
		if isMindreader {
			return mindreaderOpenEthereumLogger
		} else {
			return nodeOpenEthereumLogger
		}
	default:
		panic(fmt.Errorf("unknown node type %q, only knows about %q and %q", nodeType, "geth", "openethereum"))
	}
}

type nodeArgsByRole map[string]string

var nodeArgsByTypeAndRole = map[string]nodeArgsByRole{
	"geth": {
		"dev-miner":  "--networkid={network-id} --datadir={node-data-dir} --ipcpath={node-ipc-path} --port=" + NodeP2PPort + " --http --http.api=eth,net,web3,personal --http.port=" + NodeRPCPort + " --http.addr=0.0.0.0 --http.vhosts=* --mine --nodiscover --allow-insecure-unlock --password=/dev/null --miner.etherbase=" + devMinerAddress + " --unlock=" + devMinerAddress,
		"peering":    "--networkid={network-id} --datadir={node-data-dir} --ipcpath={node-ipc-path} --port=30304 --http --http.api=eth,net,web3 --http.port=8546 --http.addr=0.0.0.0 --http.vhosts=* --firehose-deep-mind-block-progress",
		"mindreader": "--networkid={network-id} --datadir={node-data-dir} --ipcpath={node-ipc-path} --port=" + MindreaderNodeP2PPort + " --http --http.api=eth,net,web3 --http.port=" + MindreaderNodeRPCPort + " --http.addr=0.0.0.0 --http.vhosts=* --firehose-deep-mind",
		"bootstrap":  "--networkid={network-id} --datadir={node-data-dir} --maxpeers 10 init {node-data-dir}/genesis.json",
	},
	"openethereum": {
		"peering": "--network-id={network-id} --ipc-path={node-ipc-path} --base-path={node-data-dir} --port=" + NodeP2PPort + " --jsonrpc-port=" + NodeRPCPort + " --jsonrpc-apis=web3,net,eth,parity,parity,parity_pubsub,parity_accounts,parity_set --firehose-deep-mind-block-progress",
		//"dev-miner": ...
		"mindreader": "--network-id={network-id} --ipc-path={node-ipc-path} --base-path={node-data-dir} --port=" + MindreaderNodeP2PPort + " --jsonrpc-port=" + MindreaderNodeRPCPort + " --jsonrpc-apis=web3,net,eth,parity,parity,parity_pubsub,parity_accounts,parity_set --firehose-deep-mind --no-warp",
	},
}

func registerEthereumNodeFlags(cmd *cobra.Command) error {
	registerCommonNodeFlags(cmd, false)
	cmd.Flags().String("node-role", "peering", "Sets default node arguments. accepted values: peering, dev-miner. See `sfeth help nodes` for more info")
	return nil
}

// flags common to mindreader and regular node
func registerCommonNodeFlags(cmd *cobra.Command, isMindreader bool) {
	prefix := "node-"
	managerAPIAddr := NodeManagerAPIAddr
	if isMindreader {
		prefix = "mindreader-node-"
		managerAPIAddr = MindreaderNodeManagerAPIAddr
	}

	cmd.Flags().String(prefix+"path", "geth", "command that will be launched by the node manager")
	cmd.Flags().String(prefix+"type", "geth", "one of: ['geth','openethereum']")
	cmd.Flags().String(prefix+"arguments", "", "If not empty, overrides the list of default node arguments (computed from node type and role). Start with '+' to append to default args instead of replacing. You can use the {public-ip} token, that will be matched against space-separated hostname:IP pairs in PUBLIC_IPS env var, taking hostname from HOSTNAME env var.")
	cmd.Flags().String(prefix+"data-dir", "{sf-data-dir}/{node-role}/data", "Directory for node data ({node-role} is either mindreader, peering or dev-miner)")
	cmd.Flags().String(prefix+"ipc-path", "{sf-data-dir}/{node-role}/ipc", "IPC path cannot be more than 64chars on geth")

	cmd.Flags().String(prefix+"manager-api-addr", managerAPIAddr, "Ethereum node manager API address")
	cmd.Flags().Duration(prefix+"readiness-max-latency", 30*time.Second, "Determine the maximum head block latency at which the instance will be determined healthy. Some chains have more regular block production than others.")

	cmd.Flags().String(prefix+"bootstrap-data-url", "", "URL (file or gs) to either a genesis.json file or a .tar.zst archive to decompress in the datadir. Only used when bootstrapping (no prior data)")
	cmd.Flags().String(prefix+"enforce-peers", "", "Comma-separated list of operator nodes that will be queried for an 'enode' value and added as a peer")

	cmd.Flags().StringSlice(prefix+"backups", []string{}, "Repeatable, space-separated key=values definitions for backups. Example: 'type=gke-pvc-snapshot prefix= tag=v1 freq-blocks=1000 freq-time= project=myproj'")

	cmd.Flags().Bool(prefix+"log-to-zap", true, "Enable all node logs to transit into node's logger directly, when false, prints node logs directly to stdout")
	cmd.Flags().Bool(prefix+"debug-deep-mind", false, "[DEV] Prints deep mind instrumentation logs to standard output, should be use for debugging purposes only")
}

func parseCommonNodeFlags(appLogger *zap.Logger, sfDataDir string, isMindreader bool) (
	nodePath string,
	blockmetaAddr string,
	networkID string,
	nodeType string,
	nodeDataDir string,
	nodeIPCPath string,
	debugDeepMind bool,
	logToZap bool,
	managerAPIAddress string,
	readinessMaxLatency time.Duration,
	nodeEnforcePeers string,
	bootstrapDataURL string,
	backupConfigs []string,
	shutdownDelay time.Duration,
	nodeArguments []string,
	err error,
) {
	prefix := "node-"
	nodeRole := viper.GetString("node-role")
	if isMindreader {
		prefix = "mindreader-node-"
		nodeRole = "mindreader"
	}

	nodePath = viper.GetString(prefix + "path")
	blockmetaAddr = viper.GetString("common-blockmeta-addr")
	networkID = fmt.Sprintf("%d", viper.GetUint32("common-network-id"))
	nodeType = viper.GetString(prefix + "type")
	nodeDataDir = replaceNodeRole(nodeRole,
		MustReplaceDataDir(sfDataDir, viper.GetString(prefix+"data-dir")))
	nodeIPCPath = replaceNodeRole(nodeRole,
		MustReplaceDataDir(sfDataDir, viper.GetString(prefix+"ipc-path")))
	debugDeepMind = viper.GetBool(prefix + "debug-deep-mind")
	logToZap = viper.GetBool(prefix + "log-to-zap")
	managerAPIAddress = viper.GetString(prefix + "manager-api-addr")
	readinessMaxLatency = viper.GetDuration(prefix + "readiness-max-latency")
	nodeEnforcePeers = viper.GetString(prefix + "enforce-peers")
	bootstrapDataURL = viper.GetString(prefix + "bootstrap-data-url")
	backupConfigs = viper.GetStringSlice(prefix + "backups")
	shutdownDelay = viper.GetDuration("common-system-shutdown-signal-delay") // we reuse this global value

	nodeArguments, err = buildNodeArguments(
		appLogger,
		networkID,
		nodeDataDir,
		nodeIPCPath,
		viper.GetString(prefix+"arguments"),
		nodeType,
		nodeRole,
	)

	return
}

func buildNodeArguments(appLogger *zap.Logger, networkID, nodeDataDir, nodeIPCPath, providedArgs, nodeType, nodeRole string) ([]string, error) {
	typeRoles, ok := nodeArgsByTypeAndRole[nodeType]
	if !ok {
		return nil, fmt.Errorf("invalid node type: %s", nodeType)
	}

	args, ok := typeRoles[nodeRole]
	if !ok {
		return nil, fmt.Errorf("invalid node role: %s for type %s", nodeRole, nodeType)
	}

	if providedArgs != "" {
		if strings.HasPrefix(providedArgs, "+") {
			args += " " + strings.TrimLeft(providedArgs, "+")
		} else {
			args = providedArgs // discard info provided by node type / role
		}
	}

	args = strings.Replace(args, "{node-data-dir}", nodeDataDir, -1)
	args = strings.Replace(args, "{network-id}", networkID, -1)
	args = strings.Replace(args, "{node-ipc-path}", nodeIPCPath, -1)

	if strings.Contains(args, "{public-ip}") {
		var foundPublicIP string
		hostname := os.Getenv("HOSTNAME")
		publicIPs := os.Getenv("PUBLIC_IPS") // format is PUBLIC_IPS="mindreader-v3-1:1.2.3.4 backup-node:5.6.7.8"
		for _, pairStr := range strings.Fields(publicIPs) {
			pair := strings.Split(pairStr, ":")
			if len(pair) != 2 {
				continue
			}
			if pair[0] == hostname {
				foundPublicIP = pair[1]
			}
		}

		if foundPublicIP == "" {
			appLogger.Warn("cannot find public IP in environment variable PUBLIC_IPS (format: 'HOSTNAME:a.b.c.d HOSTNAME2:e.f.g.h'), using 127.0.0.1 as fallback", zap.String("hostname", hostname))
			foundPublicIP = "127.0.0.1"
		}
		args = strings.Replace(args, "{public-ip}", foundPublicIP, -1)
	}

	return strings.Fields(args), nil
}

func buildSuperviser(
	metricsAndReadinessManager *nodeManager.MetricsAndReadinessManager,
	nodeType string,
	nodePath string,
	nodeIPCPath string,
	nodeDataDir string,
	nodeArguments []string,
	enforcedPeers string,

	appLogger, supervisedProcessLogger *zap.Logger,
	logToZap, debugDeepMind bool,
) (nodeManager.ChainSuperviser, error) {

	switch nodeType {
	case "geth":
		superviser, err := geth.NewGethSuperviser(
			nodePath,
			nodeDataDir,
			nodeIPCPath,
			nodeArguments,
			debugDeepMind,
			metricsAndReadinessManager.UpdateHeadBlock,
			enforcedPeers,
			logToZap,
			appLogger,
			supervisedProcessLogger,
		)
		if err != nil {
			return nil, fmt.Errorf("unable to create chain superviser: %w", err)
		}

		return superviser, nil
	case "openethereum":
		superviser, err := openeth.NewSuperviser(
			nodePath,
			nodeDataDir,
			nodeIPCPath,
			nodeArguments,
			debugDeepMind,
			metricsAndReadinessManager.UpdateHeadBlock,
			enforcedPeers,
			logToZap,
			appLogger, nodeGethLogger,
		)
		if err != nil {
			return nil, fmt.Errorf("unable to create chain superviser: %w", err)
		}

		return superviser, nil
	default:
		return nil, fmt.Errorf("unsupported node type: %s", nodeType)
	}
}

func buildMetricsAndReadinessManager(name string, maxLatency time.Duration) *nodeManager.MetricsAndReadinessManager {
	headBlockTimeDrift := metrics.NewHeadBlockTimeDrift(name)
	headBlockNumber := metrics.NewHeadBlockNumber(name)

	metricsAndReadinessManager := nodeManager.NewMetricsAndReadinessManager(
		headBlockTimeDrift,
		headBlockNumber,
		maxLatency,
	)
	return metricsAndReadinessManager
}

func buildChainOperator(
	superviser nodeManager.ChainSuperviser,
	metricsAndReadinessManager *nodeManager.MetricsAndReadinessManager,
	shutdownDelay time.Duration,
	bootstrapper operator.Bootstrapper,
	appLogger *zap.Logger,
) (*operator.Operator, error) {
	o, err := operator.New(
		appLogger,
		superviser,
		metricsAndReadinessManager,
		&operator.Options{
			ShutdownDelay:              shutdownDelay,
			EnableSupervisorMonitoring: true,
			Bootstrapper:               bootstrapper,
		})

	if err != nil {
		return nil, fmt.Errorf("unable to create chain operator: %w", err)
	}
	return o, nil
}

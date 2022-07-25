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
	"github.com/Shopify/sarama"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/blockstream"
	"github.com/streamingfast/dlauncher/launcher"
	nodeManager "github.com/streamingfast/node-manager"
	"github.com/streamingfast/node-manager/mindreader"
	"github.com/streamingfast/node-manager/operator"
	"github.com/streamingfast/sf-ethereum/codec"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func registerMindreaderNodeApp(backupModuleFactories map[string]operator.BackupModuleFactory) {
	launcher.RegisterApp(zlog, &launcher.AppDef{
		ID:            "mindreader-node",
		Title:         "Ethereum Mindreader Node",
		Description:   "Ethereum node with built-in operational manager and mindreader plugin to extract block data",
		RegisterFlags: registerMindreaderNodeFlags,
		InitFunc: func(runtime *launcher.Runtime) error {
			return nil
		},
		FactoryFunc: nodeFactoryFunc(true, backupModuleFactories),
	})
}

func registerMindreaderNodeFlags(cmd *cobra.Command) error {
	registerCommonNodeFlags(cmd, true)

	cmd.Flags().String("mindreader-node-grpc-listen-addr", MindreaderGRPCAddr, "Address to listen for incoming gRPC requests")
	cmd.Flags().Bool("mindreader-node-merge-and-store-directly", false, "[BATCH] When enabled, do not write oneblock files, sidestep the merger and write the merged 100-blocks logs directly to --common-blocks-store-url")
	cmd.Flags().Bool("mindreader-node-discard-after-stop-num", false, "Ignore remaining blocks being processed after stop num (only useful if we discard the mindreader data after reprocessing a chunk of blocks)")
	cmd.Flags().String("mindreader-node-working-dir", "{sf-data-dir}/mindreader/work", "Path where mindreader will stores its files")
	cmd.Flags().Uint("mindreader-node-start-block-num", 0, "Blocks that were produced with smaller block number then the given block num are skipped")
	cmd.Flags().Uint("mindreader-node-stop-block-num", 0, "Shutdown mindreader when we the following 'stop-block-num' has been reached, inclusively.")
	cmd.Flags().Int("mindreader-node-blocks-chan-capacity", 100, "Capacity of the channel holding blocks read by the mindreader. Process will shutdown superviser/geth if the channel gets over 90% of that capacity to prevent horrible consequences. Raise this number when processing tiny blocks very quickly")
	cmd.Flags().String("mindreader-node-oneblock-suffix", "default", "Unique identifier for that mindreader, so that it can produce 'oneblock files' in the same store as another instance without competing for writes.")
	cmd.Flags().Duration("mindreader-node-wait-upload-complete-on-shutdown", 30*time.Second, "When the mindreader is shutting down, it will wait up to that amount of time for the archiver to finish uploading the blocks before leaving anyway")
	cmd.Flags().Duration("mindreader-node-merge-threshold-block-age", time.Duration(math.MaxInt64), "When processing blocks with a blocktime older than this threshold, they will be automatically merged")
	cmd.Flags().String("mindreader-node-kafka-client-id", MindreaderKafkaClientId, "A user-provided string sent with every request to the brokers for logging, debugging, and auditing purposes. Defaults to \"mindreader\", but you should probably set it to something specific to your application.")
	cmd.Flags().String("mindreader-node-kafka-version", sarama.MaxVersion.String(), "Kafka version %d.%d.%d")
	cmd.Flags().StringSlice("mindreader-node-kafka-addrs", []string{"localhost:9092"}, "Kafka addresses")
	cmd.Flags().String("mindreader-node-kafka-topic", MindreaderKafkaTopic, "The Kafka topic for messages")

	return nil
}

func getMindreaderLogPlugin(
	oneBlockStoreURL string,
	mergedBlockStoreURL string,
	workingDir string,
	mergeAndStoreDirectly bool,
	mergeThresholdBlockAge time.Duration,
	batchStartBlockNum uint64,
	batchStopBlockNum uint64,
	failOnNonContiguousBlock bool,
	waitTimeForUploadOnShutdown time.Duration,
	oneBlockFileSuffix string,
	blocksChanCapacity int,
	operatorShutdownFunc func(error),
	metricsAndReadinessManager *nodeManager.MetricsAndReadinessManager,
	tracker *bstream.Tracker,
	gs *grpc.Server,
	producer codec.Producer,
	appLogger *zap.Logger) (*mindreader.MindReaderPlugin, error) {

	// It's important that this call goes prior running gRPC server since it's doing
	// some service registration. If it's call later on, the overall application exits.
	blockStreamServer := blockstream.NewServer(gs, blockstream.ServerOptionWithLogger(appLogger))

	consoleReaderFactory := func(lines chan string) (mindreader.ConsolerReader, error) {
		return codec.NewConsoleReader(lines, producer)
	}

	logPlugin, err := mindreader.NewMindReaderPlugin(
		oneBlockStoreURL,
		mergedBlockStoreURL,
		mergeAndStoreDirectly,
		mergeThresholdBlockAge,
		workingDir,
		consoleReaderFactory,
		tracker,
		batchStartBlockNum,
		batchStopBlockNum,
		blocksChanCapacity,
		metricsAndReadinessManager.UpdateHeadBlock,
		func(error) {
			operatorShutdownFunc(nil)
		},
		failOnNonContiguousBlock,
		waitTimeForUploadOnShutdown,
		oneBlockFileSuffix,
		blockStreamServer,
		appLogger,
	)
	if err != nil {
		return nil, err
	}

	return logPlugin, nil
}

func replaceNodeRole(nodeRole, in string) string {
	return strings.Replace(in, "{node-role}", nodeRole, -1)
}

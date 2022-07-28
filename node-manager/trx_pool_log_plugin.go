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

package nodemanager

import (
	"fmt"
	"strings"

	"github.com/chainstack/sf-ethereum/codec"
	"github.com/chainstack/sf-ethereum/node-manager/trxstream"
	pbcodec "github.com/chainstack/sf-ethereum/pb/sf/ethereum/codec/v1"
	pbtrxstream "github.com/chainstack/sf-ethereum/pb/sf/ethereum/trxstream/v1"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type TrxPoolLogPlugin struct {
	*shutter.Shutter

	logLines chan string
	server   *trxstream.Server
	logger   *zap.Logger
}

func NewTrxPoolLogPlugin(logger *zap.Logger) *TrxPoolLogPlugin {
	trxServer := trxstream.NewServer(logger)

	return &TrxPoolLogPlugin{
		Shutter: shutter.New(),

		server:   trxServer,
		logLines: make(chan string),
		logger:   logger,
	}
}

func (p *TrxPoolLogPlugin) Launch() {}
func (p TrxPoolLogPlugin) Stop()    {}

func (p *TrxPoolLogPlugin) Name() string {
	return "TrxPoolLogPlugin"
}
func (p *TrxPoolLogPlugin) Close(_ error) {
	p.server.Shutdown(nil)
}

func (p *TrxPoolLogPlugin) RegisterServices(gs *grpc.Server) {
	pbtrxstream.RegisterTransactionStreamServer(gs, p.server)
}

func (p *TrxPoolLogPlugin) LogLine(line string) {
	if !strings.HasPrefix(line, "DMLOG TRX_ENTER_POOL") {
		return
	}

	// The actual line without `DMLOG ` in front
	line = line[6:]
	p.logger.Debug("detected trx enter pool event detected")
	chunks, err := codec.SplitInChunks(line, 12)
	if err != nil {
		panic(fmt.Errorf("failed to spit log line %q: %w", line, err))
	}

	tx := readPoolTrxBegin(chunks)
	p.logger.Debug("pushing transaction", zap.Stringer("trx_id", codec.Hash(tx.Hash)))
	p.server.PushTransaction(tx)
}

func readPoolTrxBegin(chunks []string) *pbcodec.Transaction {
	hash := codec.FromHex(chunks[0], "TRX_POOL txHash")
	from := codec.FromHex(chunks[1], "TRX_POOL from")
	to := codec.FromHex(chunks[2], "TRX_POOL to")
	value := pbcodec.BigIntFromBytes(codec.FromHex(chunks[3], "TRX_POOL value"))
	v := codec.FromHex(chunks[4], "TRX_POOL v")
	r := codec.FromHex(chunks[5], "TRX_POOL r")
	s := codec.FromHex(chunks[6], "TRX_POOL s")
	gas := codec.FromUint64(chunks[7], "TRX_POOL gas")
	gasPrice := pbcodec.BigIntFromBytes(codec.FromHex(chunks[8], "TRX_POOL gasPrice"))
	nonce := codec.FromUint64(chunks[9], "TRX_POOL nonce")
	input := codec.FromHex(chunks[10], "TRX_POOL input")

	return &pbcodec.Transaction{
		To:       to,
		From:     from,
		Hash:     hash,
		Value:    value,
		R:        r,
		V:        v,
		S:        s,
		GasLimit: gas,
		GasPrice: gasPrice,
		Nonce:    nonce,
		Input:    input,
	}
}

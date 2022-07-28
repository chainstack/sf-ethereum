package transform

import (
	"encoding/hex"

	pbcodec "github.com/chainstack/sf-ethereum/pb/sf/ethereum/codec/v1"
	"github.com/streamingfast/bstream/transform"
	"github.com/streamingfast/dstore"
)

type LogIndexer interface {
	Add(keys []string, blockNum uint64)
}

// EthLogIndexer wraps a bstream.transform.BlockIndexer for chain-specific use on Ethereum
type EthLogIndexer struct {
	BlockIndexer LogIndexer
}

// NewEthLogIndexer instantiates and returns a new EthLogIndexer
func NewEthLogIndexer(indexStore dstore.Store, indexSize uint64) *EthLogIndexer {
	bi := transform.NewBlockIndexer(indexStore, indexSize, LogAddrIndexShortName)
	return &EthLogIndexer{
		BlockIndexer: bi,
	}
}

// ProcessBlock implements chain-specific logic for Ethereum bstream.Block's
func (i *EthLogIndexer) ProcessBlock(blk *pbcodec.Block) {
	var keys []string

	for _, trace := range blk.TransactionTraces {
		for _, log := range trace.Receipt.Logs {
			var evSig []byte
			if len(log.Topics) != 0 {
				// @todo(froch, 22022022) parameterize the topics of interest
				evSig = log.Topics[0]
			}

			keys = append(keys, hex.EncodeToString(log.Address))
			keys = append(keys, hex.EncodeToString(evSig))
		}
	}

	i.BlockIndexer.Add(keys, blk.Number)
	return
}

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

package codec

import (
	pbcodec "github.com/chainstack/sf-ethereum/pb/sf/ethereum/codec/v1"
	"github.com/streamingfast/eth-go"
)

func PBcodecLogToEOS(pblog *pbcodec.Log) *eth.Log {
	return &eth.Log{
		Address:    pblog.Address,
		Topics:     pblog.Topics,
		Data:       pblog.Data,
		Index:      pblog.Index,
		BlockIndex: pblog.BlockIndex,
	}
}

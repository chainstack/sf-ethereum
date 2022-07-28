package tools

import (
	"github.com/Shopify/sarama"
	pbcodec "github.com/chainstack/sf-ethereum/pb/sf/ethereum/codec/v1"
	"github.com/golang/protobuf/proto"
	"go.uber.org/zap"
	"time"
)

const messageVersion = 1

type KafkaProducer struct {
	topic  string
	logger *zap.Logger
	input  chan<- *sarama.ProducerMessage
}

func NewKafkaProducer(topic string, logger *zap.Logger, input chan<- *sarama.ProducerMessage) *KafkaProducer {
	return &KafkaProducer{topic: topic, logger: logger, input: input}
}

func (k KafkaProducer) Send(block *pbcodec.Block) {
	value, err := proto.Marshal(block)
	if err != nil {
		k.logger.Error(
			"error marshal block to protobuf",
			zap.Error(err),
			zap.Uint64("block_number", block.Number),
		)
		return
	}
	msg := &sarama.ProducerMessage{
		Topic:     k.topic,
		Value:     sarama.ByteEncoder(value),
		Timestamp: time.Now().UTC(),
	}

	k.input <- msg
}

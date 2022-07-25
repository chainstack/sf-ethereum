package tools

import (
	"github.com/Shopify/sarama"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

type LauncherWrapper struct {
	launcher.App
	logger   *zap.Logger
	producer sarama.AsyncProducer
}

func NewLauncherWrapper(app launcher.App, logger *zap.Logger, producer sarama.AsyncProducer) *LauncherWrapper {
	return &LauncherWrapper{App: app, logger: logger, producer: producer}
}

func (l LauncherWrapper) Terminating() <-chan struct{} {
	return l.App.Terminating()
}

func (l LauncherWrapper) Terminated() <-chan struct{} {
	return l.App.Terminated()
}

func (l LauncherWrapper) Shutdown(err error) {
	l.App.Shutdown(err)
	l.producer.AsyncClose()
	l.logger.Info("closing kafka producer...")
}

func (l LauncherWrapper) Err() error {
	return l.App.Err()
}

func (l LauncherWrapper) Run() error {
	go func() {
		for e := range l.producer.Errors() {
			l.logger.Error("kafka producer error", zap.Error(e))
		}
	}()
	return l.App.Run()
}

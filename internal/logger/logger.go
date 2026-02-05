package logger

import (
	"fmt"
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	log      *zap.Logger
	initOnce sync.Once
)

func Init(development bool) error {
	var err error
	initOnce.Do(func() {
		var cfg zap.Config
		if development {
			cfg = zap.NewDevelopmentConfig()
			cfg.EncoderConfig.TimeKey = "time"
			cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		} else {
			cfg = zap.NewProductionConfig()
			cfg.EncoderConfig.TimeKey = "time"
		}
		log, err = cfg.Build()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
			return
		}
		log.Info("Logger initialized", zap.Bool("development", development))
	})
	return err
}

func L() *zap.Logger {
	if log == nil {
		panic("Logger not initialized")
	}
	return log
}

func Sync() {
	if log != nil {
		_ = log.Sync()
	}
}

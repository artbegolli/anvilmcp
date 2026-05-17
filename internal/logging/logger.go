package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLogger creates a zap logger configured for the environment.
func NewLogger() *zap.Logger {
	config := zap.NewProductionConfig()

	// Use human-readable timestamps and level names in development
	if isDev := os.Getenv("ANVILMCP_DEV") != ""; isDev {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	logger, err := config.Build()
	if err != nil {
		panic(err)
	}

	return logger
}

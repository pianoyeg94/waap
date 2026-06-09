package ctxt

import (
	"context"

	"go.uber.org/zap"
)

func ContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func ErrorSendOrLog(errCh chan<- error, err error, logger *zap.Logger, ctx context.Context) (ctxDone bool) {
	select {
	case errCh <- err:
		return false
	case <-ctx.Done():
		logger.Error("Encountered an error", zap.Error(err))
		return true
	}
}

package waap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/pianoyeg94/waap/pkg/ctxt"
)

const (
	errCorpusStreamerStreamingTemplate           = "corpusStreamer encountered error streaming corpus streams: %w"
	errCorpusStreamerConvertingToAbsPathTemplate = "corpusStreamer encountered error converting relative to absolute path: %w"
	errCorpusStreamerOpeningStreamTemplate       = "corpusStreamer encountered opening corpus stream: %w"
)

var (
	ErrCorpusStreamerAllreadyClosed = errors.New("corpusStreamer already closed")
	errContextDone                  = errors.New("context done")
)

type corpusStream struct {
	name   string
	stream io.ReadSeekCloser
}

type corpusStreamer struct {
	dirName    string
	isClosed   bool
	errorsSink chan<- error
	logger     *zap.Logger
	closeCtx   context.Context
}

func newCorpusStreamer(dirName string, errorsSink chan<- error, logger *zap.Logger, closeCtx context.Context) *corpusStreamer {
	return &corpusStreamer{
		dirName:    dirName,
		errorsSink: errorsSink,
		logger:     logger,
		closeCtx:   closeCtx,
	}
}

func (p *corpusStreamer) startStreamingCorpuses(wg *sync.WaitGroup) (<-chan *corpusStream, error) {
	if p.isClosed {
		return nil, ErrCorpusStreamerAllreadyClosed
	}
	p.isClosed = true

	corpusStreams := make(chan *corpusStream)
	wg.Go(func() {
		defer func() {
			if err := recover(); err != nil {
				_ = ctxt.ErrorSendOrLog(p.errorsSink, fmt.Errorf(errWaapPanickedTemplate, err), p.logger, p.closeCtx)
			}
			close(corpusStreams)
		}()

		if err := filepath.WalkDir(p.dirName, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf(errCorpusStreamerStreamingTemplate, err)
			}
			if d.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}

			filePath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf(errCorpusStreamerConvertingToAbsPathTemplate, err)
			}
			jsonFile, err := os.OpenFile(filePath, os.O_RDONLY, 0444)
			if err != nil {
				return fmt.Errorf(errCorpusStreamerOpeningStreamTemplate, err)
			}

			select {
			case corpusStreams <- &corpusStream{
				name:   filePath,
				stream: jsonFile,
			}:
				if ctxt.ContextDone(p.closeCtx) {
					return errContextDone
				}
			case <-p.closeCtx.Done():
				return errContextDone
			}
			return nil
		}); err != nil {
			if err != errContextDone {
				_ = ctxt.ErrorSendOrLog(p.errorsSink, err, p.logger, p.closeCtx)
			}
		}
	})

	return corpusStreams, nil
}

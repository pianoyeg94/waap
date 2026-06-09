package waap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"go.uber.org/zap"

	"github.com/pianoyeg94/waap/pkg/ctxt"
	"github.com/pianoyeg94/waap/pkg/metrics"
)

const (
	errWaapPanickedTemplate             = "Waap paniced: %v"
	errWaapOpeningBadgerDBTemplate      = "Wapp encountered error opening BadgerDB: %w"
	errWaapClosingBadgerDBTemplate      = "Wapp encountered error closing BadgerDB: %w"
	errWaapBuildingStatsMessageTemplate = "Waap encountered error building stats message: %w"
)

type Waap struct {
	nmapProbes          nmapProbes
	badgerDB            *badger.DB
	badgerKey           uint64
	badgerDataDir       string
	corpusStreamer      *corpusStreamer
	requestStreamers    []*requestStreamer
	nmapProbers         []*nmapProber
	finalRpsTracker     *metrics.EventTracker
	rpsTracker          *metrics.EventTracker
	logger              *zap.Logger
	probeNumRequestsMax int

	corpusStreamsSink        chan *corpusStream
	requestsSink             chan string
	processedStreamsSink     chan int
	processedAllStreamsEvent chan struct{}
	processedRequestEvent    chan struct{}
	errorsSink               chan error

	processedStreamsCount  int
	totalRequestCount      int
	processedRequestsCount int
	totalTimElapsedMinutes float64
	avgRPS                 float64
	err                    error

	finishedSignal chan struct{}
	finishedMsg    string
	closeCtx       context.Context
	close          context.CancelFunc
	closeWg        sync.WaitGroup
}

func NewWaap(nmapServiceProbesFile io.ReadCloser, httpCorpusesDir string, badgerDataDir string, probeNumRequestsMax int, isSIMDSupported bool, logger *zap.Logger, closeCtx context.Context) (*Waap, error) {
	var err error
	badgerDB, err := openBadgerDB(badgerDataDir)
	if err != nil {
		return nil, fmt.Errorf(errWaapOpeningBadgerDBTemplate, err)
	}
	defer func() {
		if err != nil {
			badgerDB.Close()
		}
	}()
	var badgerKey uint64

	var nmapProbes nmapProbes
	probesParser := newNmapParser(nmapServiceProbesFile)
	if nmapProbes, err = probesParser.parse(); err != nil {
		return nil, err
	}

	closeCtx, close := context.WithCancel(closeCtx)
	maxprocs := runtime.GOMAXPROCS(0)
	corpusStreamsSink := make(chan *corpusStream)
	processedStreamsSink := make(chan int, maxprocs)
	requestsSink := make(chan string)
	processedRequestEvent := make(chan struct{}, maxprocs)
	errorsSink := make(chan error, 1)

	corpusStreamer := newCorpusStreamer(httpCorpusesDir, errorsSink, logger, closeCtx)
	requestStreamers := make([]*requestStreamer, 0, maxprocs)
	nmapProbers := make([]*nmapProber, 0, maxprocs)
	for range maxprocs {
		requestStreamers = append(requestStreamers, newRequestStreamer(
			corpusStreamsSink,
			logger,
			processedStreamsSink,
			requestsSink,
			errorsSink,
			isSIMDSupported,
			closeCtx,
		))
	}
	for range maxprocs {
		nmapProbers = append(nmapProbers, newNmapProber(
			nmapProbes,
			badgerDB,
			&badgerKey,
			logger,
			requestsSink,
			errorsSink,
			processedRequestEvent,
			closeCtx,
		))
	}

	return &Waap{
		nmapProbes:          nmapProbes,
		badgerDB:            badgerDB,
		badgerKey:           badgerKey,
		corpusStreamer:      corpusStreamer,
		requestStreamers:    requestStreamers,
		nmapProbers:         nmapProbers,
		finalRpsTracker:     metrics.NewEventTracker(),
		rpsTracker:          metrics.NewEventTracker(),
		logger:              logger,
		probeNumRequestsMax: probeNumRequestsMax,

		corpusStreamsSink:        corpusStreamsSink,
		processedStreamsSink:     processedStreamsSink,
		requestsSink:             requestsSink,
		processedAllStreamsEvent: make(chan struct{}),
		processedRequestEvent:    processedRequestEvent,
		errorsSink:               errorsSink,

		finishedSignal: make(chan struct{}),
		closeCtx:       closeCtx,
		close:          close,
	}, nil
}

func (w *Waap) StartProbingRequests() error {
	w.logger.Info("Starting to probe http-corpus streams")
	corpusStreams, err := w.corpusStreamer.startStreamingCorpuses(&w.closeWg)
	if err != nil {
		return err
	}
	w.watchFinished()

	w.startStreamingCorpusStreams(corpusStreams)
	for _, streamer := range w.requestStreamers {
		streamer.startStreamingRequestToSink(&w.closeWg)
	}
	for _, prober := range w.nmapProbers {
		prober.startProbingRequests(&w.closeWg)
	}
	return nil
}

func (w *Waap) FinishedSignal() <-chan struct{} {
	return w.finishedSignal
}

func (w *Waap) Close() error {
	w.logger.Info("Finished probing http-corpus streams, waiting for all workers to shut down")

	w.close()
	w.closeWg.Wait()
	close(w.corpusStreamsSink)
	close(w.requestsSink)
	close(w.processedStreamsSink)
	close(w.processedRequestEvent)
	close(w.errorsSink)

	if statsMsg, err := w.buildStatsMessage(); err != nil {
		w.logStatsMsgAsFallback(err)
	} else {
		fmt.Print(statsMsg)
	}

	if err := w.badgerDB.Close(); err != nil {
		return fmt.Errorf(errWaapClosingBadgerDBTemplate, err)
	}
	return nil
}

func (w *Waap) startStreamingCorpusStreams(corpusStreams <-chan *corpusStream) {
	w.closeWg.Go(func() {
		defer func() {
			if err := recover(); err != nil {
				_ = ctxt.ErrorSendOrLog(w.errorsSink, fmt.Errorf(errWaapPanickedTemplate, err), w.logger, w.closeCtx)
			}
			close(w.processedAllStreamsEvent)
		}()
		for {
			select {
			case corpusStream, ok := <-corpusStreams:
				if !ok {
					return
				}
				if ctxt.ContextDone(w.closeCtx) {
					corpusStream.stream.Close()
				}
				select {
				case w.corpusStreamsSink <- corpusStream:
					if ctxt.ContextDone(w.closeCtx) {
						return
					}
				case <-w.closeCtx.Done():
					return
				}
			case <-w.closeCtx.Done():
				return
			}
		}
	})
}

func (w *Waap) watchFinished() {
	w.closeWg.Go(func() {
		ticker := time.NewTicker(10 * time.Second)
		start := time.Now()
		defer func() {
			if err := recover(); err != nil {
				errors.Join(w.err, fmt.Errorf(errWaapPanickedTemplate, err))
			}
			w.totalTimElapsedMinutes = time.Since(start).Minutes()
			w.avgRPS = w.finalRpsTracker.Rate()
			close(w.finishedSignal)
			ticker.Stop()
		}()

		for {
			select {
			case <-w.processedAllStreamsEvent:
				if w.processedAllRequests() {
					return
				}
				for {
					select {
					case reqCount := <-w.processedStreamsSink:
						w.totalRequestCount += reqCount
						w.processedStreamsCount++
						if ctxt.ContextDone(w.closeCtx) {
							return
						}
					case <-w.processedRequestEvent:
						w.processedRequestsCount++
						w.finalRpsTracker.Inc()
						w.rpsTracker.Inc()
						if ctxt.ContextDone(w.closeCtx) || w.processedAllRequests() {
							return
						}
					case w.err = <-w.errorsSink:
						return
					case <-ticker.C:
						w.logger.Info("Processed requests", zap.Int("count", w.processedRequestsCount))
						w.logger.Info("Current AVG RPS", zap.Float64("rps", w.rpsTracker.Rate()))
						if ctxt.ContextDone(w.closeCtx) {
							return
						}
					case <-w.closeCtx.Done():
						return
					}
				}
			case reqCount := <-w.processedStreamsSink:
				w.totalRequestCount += reqCount
				w.processedStreamsCount++
				if ctxt.ContextDone(w.closeCtx) {
					return
				}
			case <-w.processedRequestEvent:
				w.processedRequestsCount++
				w.finalRpsTracker.Inc()
				w.rpsTracker.Inc()
				if ctxt.ContextDone(w.closeCtx) || w.processedAllRequests() {
					return
				}
			case w.err = <-w.errorsSink:
				return
			case <-ticker.C:
				w.logger.Info("Processed requests", zap.Int("count", w.processedRequestsCount))
				w.logger.Info("Current AVG RPS", zap.Float64("rps", w.rpsTracker.Rate()))
				if ctxt.ContextDone(w.closeCtx) {
					return
				}
			case <-w.closeCtx.Done():
				return
			}

		}
	})
}

func (w *Waap) processedAllRequests() bool {
	return (w.processedRequestsCount != 0 && w.totalRequestCount == w.processedRequestsCount) ||
		(w.probeNumRequestsMax > 0 && w.processedRequestsCount >= w.probeNumRequestsMax)
}

func (w *Waap) buildStatsMessage() (string, error) {
	var msgBuilder strings.Builder
	msgBuilder.WriteByte('\n')
	if w.err != nil {
		if _, err := msgBuilder.WriteString("Completed with error: "); err != nil {
			return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
		}
		if _, err := msgBuilder.WriteString(w.err.Error()); err != nil {
			return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
		}
		msgBuilder.WriteByte('\n')
	}

	if _, err := msgBuilder.WriteString("Processed a total of "); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	if _, err := msgBuilder.WriteString(strconv.Itoa(w.processedStreamsCount)); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	if _, err := msgBuilder.WriteString(" http corpus streams\n"); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}

	if _, err := msgBuilder.WriteString("Processed a total of "); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	if _, err := msgBuilder.WriteString(strconv.Itoa(w.processedRequestsCount)); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	if _, err := msgBuilder.WriteString(" http requests\n"); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}

	if _, err := msgBuilder.WriteString("Total time taken in minutes: "); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	if _, err := msgBuilder.WriteString(strconv.FormatFloat(w.totalTimElapsedMinutes, 'f', -1, 64)); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	msgBuilder.WriteByte('\n')

	if _, err := msgBuilder.WriteString("AVG RPS: "); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	if _, err := msgBuilder.WriteString(strconv.FormatFloat(w.avgRPS, 'f', -1, 64)); err != nil {
		return "", fmt.Errorf(errWaapBuildingStatsMessageTemplate, err)
	}
	msgBuilder.WriteByte('\n')

	return msgBuilder.String(), nil
}

func (w *Waap) logStatsMsgAsFallback(err error) {
	w.logger.Error("Error building stats message", zap.String("error", err.Error()))
	if w.err != nil {
		w.logger.Error("Completed with error", zap.String("error", w.err.Error()))
		w.logger.Info("Processed a total of http corpus streams", zap.Int("streams_count", w.processedStreamsCount))
		w.logger.Info("Processed a total of http request", zap.Int("request_count", w.processedRequestsCount))
		w.logger.Info("Total time taken in minutes", zap.Float64("total_time", w.totalTimElapsedMinutes))
		w.logger.Info("AVG RPS", zap.Float64("avg_rps", w.avgRPS))
	}
}

func openBadgerDB(path string) (*badger.DB, error) {
	os.RemoveAll(path)
	opts := badger.DefaultOptions(path).WithLoggingLevel(badger.ERROR)
	return badger.Open(opts)
}

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	protobuf "google.golang.org/protobuf/proto"

	"github.com/pianoyeg94/waap/pkg/ctxt"
	"github.com/pianoyeg94/waap/proto"
)

const (
	errWriterWrapperSyncingFileTemplate   = "writerWrapper encountered error syncing file %s: %w"
	errWriterWrapperClosingFileTemplate   = "writerWrapper encountered error closing file %s: %w"
	errWriterWrapperWritingToFileTemplate = "writerWrapper encountered error writing to file %s: %w"
	errWriterWrapperPrefix                = "writerWrapper"

	errDumpJsonResultsOpenFileTemplate = "dumpJsonResults encountered error opening file %s: %w"
	errDumpJsonPrefix                  = "dumpJsonResults"

	errJsonEncoderEncodeTemplate = "json.Encoder encountered error encoding data to file %s: %w"
	errJsonEncoderPrefix         = "json.Encoder"

	errProtobufUnmarhsallTemplate = "proto.Unmarshall encountered error unmarhsalling: %w"
	errProtobufUnmarhsallPrefix   = "proto.Unmarshall"

	errBadgerDBOpenTemplate            = "BadgerDB encountered error while opening: %w"
	errBadgerDBRetrievingValueTemplate = "BadgerDB encountered error retrieving value: %w"
	errBadgerDBRetrievingValuePrefix   = "BadgerDB encountered error retrieving value"
	errBadgerDBTransactionTemplate     = "BadgerDB encountered a trasnaction error: %w"

	errMakingOrFindindResultsDirectoryTemplate = "encountered error making or finding results directory: %w"
)

var (
	errorWriterWrapperFileNotSet = errors.New("writerWrapper: file not set")

	resultsDirPath string

	exportResultsCmd = &cobra.Command{
		Use:   "export-results",
		Long:  "Exports nmap probe results to json files",
		Short: "Exports nmap probe results to json files",
		RunE: func(cmd *cobra.Command, args []string) error {
			go func() { cmd.Parent().RunE(cmd, args) }() // run pprof server

			logger := cmd.Context().Value(LoggerKey).(*zap.Logger)
			ctxCancel := cmd.Context().Value(ContextCancelFuncKey).(context.CancelFunc)
			defer ctxCancel()

			opts := badger.DefaultOptions(badgerDBDataDirPath).WithLoggingLevel(badger.ERROR)
			db, err := badger.Open(opts)
			if err != nil {
				return fmt.Errorf(errBadgerDBOpenTemplate, err)
			}
			defer db.Close()

			if err := os.MkdirAll(resultsDirPath, 0755); err != nil {
				return fmt.Errorf(errMakingOrFindindResultsDirectoryTemplate, err)
			}

			logger.Info("Starting exporting results to directory", zap.String("directory", resultsDirPath))
			var writer *writerWrapper
			var encoder *json.Encoder
			resultsCounter := 1
			results := proto.NmapProbeResults{
				Results: make([]*proto.RequestProbe, 0, 1000),
			}
			defer func() {
				if len(results.Results) > 0 {
					if err = dumpJsonResults(&encoder, &writer, &results, &resultsCounter, logger); err != nil {
						logger.Error(err.Error())
					}
				}
				logger.Info("Finished exporting results to directory", zap.String("directory", resultsDirPath), zap.Int("file_count", resultsCounter-1))
			}()

			if err = db.View(func(txn *badger.Txn) error {
				opts := badger.DefaultIteratorOptions
				it := txn.NewIterator(opts)
				defer it.Close()
				for it.Rewind(); it.Valid(); it.Next() {
					if ctxt.ContextDone(cmd.Context()) {
						return nil
					}
					item := it.Item()
					if err := item.Value(func(val []byte) error {
						var probe proto.RequestProbe
						if err := protobuf.Unmarshal(val, &probe); err != nil {
							return fmt.Errorf(errProtobufUnmarhsallTemplate, err)
						}
						results.Results = append(results.Results, &probe)
						return nil
					}); err != nil {
						if !strings.Contains(err.Error(), errProtobufUnmarhsallPrefix) {
							return fmt.Errorf(errBadgerDBRetrievingValueTemplate, err)
						}
						return err
					}
					if len(results.Results) == cap(results.Results) {
						if err := dumpJsonResults(&encoder, &writer, &results, &resultsCounter, logger); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				if !(strings.Contains(err.Error(), errProtobufUnmarhsallPrefix) ||
					strings.Contains(err.Error(), errBadgerDBRetrievingValuePrefix) ||
					strings.Contains(err.Error(), errWriterWrapperPrefix) ||
					strings.Contains(err.Error(), errDumpJsonPrefix) ||
					strings.Contains(err.Error(), errJsonEncoderPrefix)) {
					return fmt.Errorf(errBadgerDBTransactionTemplate, err)
				}
				return err
			}
			return nil
		},
	}
)

type writerWrapper struct {
	filename string
	writer   *os.File
}

func newWriterWrapper(writer *os.File, filename string) *writerWrapper {
	return &writerWrapper{writer: writer, filename: filename}
}

func (w *writerWrapper) Write(data []byte) (n int, err error) {
	if w.writer == nil {
		return 0, errorWriterWrapperFileNotSet
	}
	if n, err = w.writer.Write(data); err != nil {
		return 0, fmt.Errorf(errWriterWrapperWritingToFileTemplate, w.filename, err)
	}
	return n, nil
}

func (w *writerWrapper) resetWriter(writer *os.File, filename string) error {
	if w.writer != nil {
		if err := w.writer.Sync(); err != nil {
			return fmt.Errorf(errWriterWrapperSyncingFileTemplate, w.filename, err)
		}
		if err := w.writer.Close(); err != nil {
			return fmt.Errorf(errWriterWrapperClosingFileTemplate, w.filename, err)
		}
	}
	w.filename = filename
	w.writer = writer
	return nil
}

func (w *writerWrapper) reset() error {
	if w.writer != nil {
		if err := w.writer.Sync(); err != nil {
			return fmt.Errorf(errWriterWrapperSyncingFileTemplate, w.filename, err)
		}
		if err := w.writer.Close(); err != nil {
			return fmt.Errorf(errWriterWrapperClosingFileTemplate, w.filename, err)
		}
	}
	w.filename = ""
	w.writer = nil
	return nil
}

func dumpJsonResults(encoderPtr **json.Encoder, writerPtr **writerWrapper, results *proto.NmapProbeResults, resultsCounter *int, logger *zap.Logger) error {
	filePath := filepath.Join(resultsDirPath, fmt.Sprintf("result_%d.json", *resultsCounter))
	resultsFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		return fmt.Errorf(errDumpJsonResultsOpenFileTemplate, filePath, err)
	}

	var writer *writerWrapper
	if *writerPtr == nil {
		*writerPtr = newWriterWrapper(resultsFile, filePath)
	} else {
		(*writerPtr).resetWriter(resultsFile, filePath)
	}
	writer = *writerPtr
	defer func() { _ = writer.reset() }()

	var encoder *json.Encoder
	if *encoderPtr == nil {
		*encoderPtr = json.NewEncoder(writer)
		(*encoderPtr).SetIndent("", "  ")
		(*encoderPtr).SetEscapeHTML(false)
	}
	encoder = *encoderPtr

	if err := encoder.Encode(results); err != nil {
		return fmt.Errorf(errJsonEncoderEncodeTemplate, filePath, err)
	}

	*resultsCounter++
	logger.Info("Dumped nmap probe results to file", zap.String("filepath", filePath), zap.Int("probe_count", len(results.Results)))
	results.Results = results.Results[:0]
	return nil
}

func init() {
	exportResultsCmd.Flags().StringVar(&badgerDBDataDirPath, "badger-data-dir", "./badger", "Path to BadgerDB data directory")
	exportResultsCmd.Flags().StringVar(
		&resultsDirPath,
		"results-dir-path",
		"./results",
		"Path to results directory with json files results, will be created if not exists",
	)
	rootCmd.AddCommand(exportResultsCmd)
}

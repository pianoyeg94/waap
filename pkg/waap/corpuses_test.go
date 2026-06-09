package waap

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func TestCorpusStreamerStartStreamingCorpuses(t *testing.T) {
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	errorsSink := make(chan error, 1)
	defer func() { close(errorsSink) }()

	logger, err := zap.NewDevelopment()
	if err != nil {
		t.Fatalf("Failed creating logger: %v", err)
	}

	corpusStreamer := newCorpusStreamer("./test-data", errorsSink, logger, ctx)
	corpusStreams, err := corpusStreamer.startStreamingCorpuses(&wg)
	if err != nil {
		t.Fatalf("Failed starting to stream corpus streams: %v", err)
	}

	const expectedCorpusStreamName = "browsing_2024_4shared.json"
	select {
	case corpusStream := <-corpusStreams:
		corpusStream.stream.Close()
		if filepath.Base(corpusStream.name) != expectedCorpusStreamName {
			t.Errorf("corpusStreamer created an invalid stream = %s, want %s", corpusStream.name, expectedCorpusStreamName)
		}
	case err := <-errorsSink:
		t.Fatalf("Received error from corpusStreamer: %v", err)
	}

	cancel()
	wg.Wait()
}
